//! Anti-abuse layer for the tracking endpoints.
//!
//! Two independent controls, applied before an event reaches Kafka:
//! - per-source rate limiting (fixed 60s window, bounded cache)
//! - prefetch / scanner filtering (the response is still served so real
//!   clients never break; only the analytics event is suppressed)
//!
//! Open-redirect protection lives in the ticket design itself (`links.rs`):
//! destinations never travel inside the URL, so there is nothing to forge.

use axum::http::HeaderMap;
use moka::future::Cache;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Arc;
use std::time::Duration;

/// Fixed-window per-source request counter. Window resets via entry TTL, the
/// cache is hard-capped so a botnet rotating sources cannot exhaust memory.
pub struct RateLimiter {
    buckets: Cache<String, Arc<AtomicU32>>,
    limit: u32,
}

impl RateLimiter {
    pub fn new(limit_per_min: u32) -> Self {
        Self {
            buckets: Cache::builder()
                .max_capacity(50_000)
                .time_to_live(Duration::from_secs(60))
                .build(),
            limit: limit_per_min,
        }
    }

    /// Returns true while the source is within its per-minute budget.
    pub async fn allow(&self, source: &str) -> bool {
        let counter = self
            .buckets
            .get_with(source.to_string(), async { Arc::new(AtomicU32::new(0)) })
            .await;
        counter.fetch_add(1, Ordering::Relaxed) < self.limit
    }
}

/// Browser/link-warming prefetches and previews: the fetch is speculative,
/// not a human open/click, so it must not count.
pub fn is_prefetch(headers: &HeaderMap) -> bool {
    for name in ["sec-purpose", "purpose", "x-purpose", "x-moz"] {
        if let Some(value) = headers.get(name).and_then(|h| h.to_str().ok()) {
            let value = value.to_ascii_lowercase();
            if value.contains("prefetch")
                || value.contains("preview")
                || value.contains("prerender")
            {
                return true;
            }
        }
    }
    false
}

/// UA markers for crawlers, CLI clients, link-expanding chat apps, uptime
/// monitors, and email security gateways that follow every link in a message.
/// Gmail's image proxy is deliberately NOT listed: it is the only open signal
/// Gmail exposes, and filtering it would zero out opens for Gmail recipients.
const SCANNER_UA_MARKERS: &[&str] = &[
    "bot",
    "spider",
    "crawl",
    "curl/",
    "wget/",
    "python-requests",
    "python/",
    "go-http-client",
    "okhttp",
    "java/",
    "headless",
    "phantomjs",
    "validator",
    "pingdom",
    "uptime",
    "statuscake",
    "site24x7",
    "bingpreview",
    "skypeuripreview",
    "whatsapp",
    "telegram",
    // email security gateways / link rewriters
    "urldefense",
    "safelinks",
    "barracuda",
    "mimecast",
    "proofpoint",
    "forcepoint",
    "symantec",
    "trendmicro",
    "sophos",
    "zscaler",
];

/// Phone makers whose device name, sent in a WebView or older browser UA,
/// contains a marker above. Removed before matching so a person's open counts.
const DEVICE_UA_NAMES: &[&str] = &["cubot"];

pub fn is_scanner(user_agent: Option<&str>) -> bool {
    let Some(ua) = user_agent else {
        // No UA at all is never a real mail client or browser.
        return true;
    };
    let ua = DEVICE_UA_NAMES
        .iter()
        .fold(ua.to_ascii_lowercase(), |ua, name| ua.replace(name, " "));
    SCANNER_UA_MARKERS.iter().any(|marker| ua.contains(marker))
}

#[cfg(test)]
mod tests {
    use super::{is_prefetch, is_scanner};
    use axum::http::{HeaderMap, HeaderValue};

    #[test]
    fn announced_fetchers_are_scanners() {
        for ua in [
            "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
            "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
            "Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)",
            "Twitterbot/1.0",
            "TelegramBot (like TwitterBot)",
            "curl/8.4.0",
            "python-requests/2.31.0",
            "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/120.0.0.0 Safari/537.36",
        ] {
            assert!(is_scanner(Some(ua)), "{ua}");
        }
        assert!(is_scanner(None));
    }

    #[test]
    fn people_and_the_gmail_proxy_are_not_scanners() {
        for ua in [
            "Mozilla/5.0 (Windows NT 5.1; rv:11.0) Gecko Firefox/11.0 (via ggpht.com GoogleImageProxy)",
            "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1",
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
            "Mozilla/5.0 (Linux; Android 11; CUBOT_X50 Build/RP1A.200720.011; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/119.0.6045.163 Mobile Safari/537.36",
            "Mozilla/5.0 (Linux; Android 10; CUBOT KINGKONG 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/96.0.4664.104 Mobile Safari/537.36",
        ] {
            assert!(!is_scanner(Some(ua)), "{ua}");
        }
    }

    #[test]
    fn removing_a_phone_name_neither_hides_nor_makes_a_marker() {
        assert!(is_scanner(Some(
            "Mozilla/5.0 (Linux; Android 11; CUBOT_X50) compatible; Googlebot/2.1"
        )));
        assert!(is_scanner(Some("cubotbot/1.0")));
        // The name is replaced by a separator, so its neighbours never join into a marker.
        assert!(!is_scanner(Some(
            "Mozilla/5.0 (Linux; Android 11; bcubotot)"
        )));
    }

    #[test]
    fn prefetch_headers_are_prefetches() {
        for (name, value) in [
            ("sec-purpose", "prefetch;prerender"),
            ("purpose", "prefetch"),
            ("x-purpose", "preview"),
            ("x-moz", "prefetch"),
        ] {
            let mut headers = HeaderMap::new();
            headers.insert(name, HeaderValue::from_static(value));
            assert!(is_prefetch(&headers), "{name}: {value}");
        }
        let mut headers = HeaderMap::new();
        headers.insert("sec-purpose", HeaderValue::from_static("navigate"));
        assert!(!is_prefetch(&headers));
        assert!(!is_prefetch(&HeaderMap::new()));
    }
}
