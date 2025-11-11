# 🔌🚰 Nestanak-Info Service

A monitoring service that checks URLs for specific search terms and sends email alerts when they are found. Monitors **power outages** (Elektrodistribucija) and **water outages** (Beogradski Vodovod) for Serbian cities, with smart extraction of dates, times, and locations.

## Features

- **State Persistence**: Survives restarts - remembers sent emails and seen incidents
- **Smart Deduplication**: Won't send duplicate emails for the same incident (7-day window)
- **Per-URL Configuration**: Each URL has its own search terms - perfect for monitoring different services
- **Granular Search Terms**: Power URLs can search for "Земун" while water URLs search for "Водовод"
- **Timezone Support**: Configure time offset so all displayed times match your local timezone
- **DNS Caching**: Reduces DNS queries, provides fallback, and detects IP changes (5-minute default)
- **Smart Email Limiting**: Maximum 2 emails per URL per day (configurable) - no spam!
- **Email Alerts**: Detailed notifications via Brevo with extracted date, time, and address
- **HTML Parsing**: Intelligent extraction of information from web pages
- **Connection Monitoring**: Alerts when URLs become unreachable and when they recover
- **Rate Limiting**: Global hourly limit + per-URL daily limit
- **Worker Pool**: Concurrent URL checking with configurable workers
- **Web Interface**: Shows each URL with its specific search terms and status
- **Systemd Integration**: Runs as a system service with automatic restart
- **Secure Authentication**: Argon2id password hashing with session management
- **Match History**: Track and display recent matches (48 hours by default)

## Installation

### Prerequisites

- Linux system with systemd
- Go 1.21 or later (will be installed automatically if missing)
- Root access for installation

### Quick Install

1. Clone or download this repository
2. Edit `config.json` with your settings:
   - Add your Brevo API key
   - Configure recipient email addresses
   - Set your search terms
   - Configure URLs to monitor

3. Run the installer:
```bash
sudo ./install.sh
```

4. Start the service:
```bash
sudo systemctl start nestanak-info
```

## Configuration

Edit `/opt/nestanak-info/config.json`:

```json
{
  "check_interval_seconds": 300,
  "max_emails_per_url_per_day": 2,
  "url_configs": [
    {
      "url": "https://elektrodistribucija.rs/planirana-iskljucenja-beograd/Dan_1_Iskljucenja.htm",
      "name": "Power - Day 1",
      "search_terms": ["Земун", "Насеље БАТАЈНИЦА:"]
    },
    {
      "url": "https://watercompany.com/outages.html",
      "name": "Water Outages",
      "search_terms": ["Батајница", "Земун"]
    }
  ],
  "recipients": ["your-email@example.com"],
  "brevo_api_key": "YOUR_BREVO_API_KEY_HERE",
  "sender_email": "alerts@yourdomain.com",
  "sender_name": "Nestanak Notifier"
}
```

### Key Configuration Options

#### Global Settings
- `check_interval_seconds`: How often to check URLs (default: 300 = 5 minutes)
- `alert_cooldown_minutes`: Minimum time between alerts for same URL (default: 60)
- `email_rate_limit_per_hour`: Maximum emails globally per hour (default: 20)
- `max_emails_per_url_per_day`: **Maximum emails per URL per day** (default: 2) - prevents spam
- `max_concurrent_checks`: Number of concurrent URL checks (default: 5)
- `connect_timeout`: HTTP request timeout in seconds (default: 8)
- `time_offset_hours`: Timezone offset in hours from server time (default: 0, range: -12 to +14)
  - Example: If server is UTC and you're in CET (UTC+1), set to `1`
  - Example: If server is UTC and you're in EST (UTC-5), set to `-5`
- `dns_cache_ttl_minutes`: DNS cache TTL in minutes (default: 5, range: 1-1440)
  - Reduces DNS queries and provides fallback when DNS fails
  - Detects and logs IP changes (useful for DDNS hosts)
- `user_agent_rotation_enabled`: Enable/disable User-Agent rotation (default: true)
  - When enabled: Fetches recent User-Agents from GitHub on startup
  - When disabled: Uses static hardcoded User-Agent
- `user_agent_pool_size`: Number of User-Agents to rotate through (default: 6, range: 1-100)
  - Higher number = more diversity, less predictable pattern
  - Lower number = simpler rotation, faster startup
  - Max 100 (uses all available agents from source)
- `state_file_path`: Path to persistent state file (default: `state.json`)
  - Relative path resolves to `/opt/nestanak-info/state.json`
  - Stores email counts, seen matches (with hashes), and alert times
  - Survives service restarts, system reboots, and updates
  - Prevents duplicate emails for same incident across restarts
  - Auto-cleanup of old data (>24h for email counts, >7d for match history)

#### Per-URL Configuration
Each URL can have its own configuration:
- `url`: The URL to monitor (required)
- `name`: Friendly name for the URL (optional)
- `search_terms`: Array of search terms **specific to this URL** (required)

**Example**: Power outages might search for ["Земун", "БАТАЈНИЦА"], while water outages search for ["Батајница", "Водовод"]

#### Web Interface
- `http_enabled`: Enable web interface (default: true)
- `http_listen`: Web interface address (default: "127.0.0.1:8081")
- `auth_enabled`: Require password for web interface (default: false)
- `recent_matches_hours`: How many hours of match history to keep (default: 48)

### Timezone Configuration

All displayed times (web UI, emails, logs) use the `time_offset_hours` setting:

**Example: You're in Serbia (CET/CEST - UTC+1 or UTC+2)**
```json
{
  "time_offset_hours": 1  // Winter (CET - UTC+1)
  // or
  "time_offset_hours": 2  // Summer (CEST - UTC+2)
}
```

**Example: Server is in New York (EST - UTC-5), you're in California (PST - UTC-8)**
```json
{
  "time_offset_hours": -3  // 3 hours behind server
}
```

**Common timezones:**
- UTC+1 (CET - Central European Time - Belgrade): `1`
- UTC+2 (CEST - Summer time - Belgrade): `2`
- UTC+3 (MSK - Moscow): `3`
- UTC-5 (EST - Eastern US): `-5`
- UTC-8 (PST - Pacific US): `-8`

## Web Interface

Access the web interface at `http://127.0.0.1:8081` (or configured address).

The interface shows:
- Service status and uptime
- Monitored URLs and their status
- Search terms being monitored
- Recent matches (when search terms were found - last 48 hours by default)

### Enable Authentication

1. Generate a password hash:
```bash
cd /opt/nestanak-info
sudo -u nestanak ./nestanak-info -set-password
```

2. Copy the generated hash to `config.json`:
```json
{
  "auth_enabled": true,
  "password_hash": "$argon2id$v=19$m=65536,t=3,p=4$...",
  "session_timeout_minutes": 60
}
```

3. Restart the service:
```bash
sudo systemctl restart nestanak-info
```

## Management Commands

```bash
# Start service
sudo systemctl start nestanak-info

# Stop service
sudo systemctl stop nestanak-info

# Restart service
sudo systemctl restart nestanak-info

# Check status
sudo systemctl status nestanak-info

# View logs
sudo journalctl -u nestanak-info -f

# View recent logs
sudo journalctl -u nestanak-info -n 100
```

## Updating

To update the service with new code:

1. Pull latest code
2. Run the installer again:
```bash
sudo ./install.sh
```

The installer will:
- Preserve your existing configuration
- Rebuild the service binary
- Update dependencies
- Restart the service

## Uninstalling

To completely remove the service:

```bash
sudo ./uninstall.sh
```

This will remove:
- The service and systemd configuration
- The service user
- The installation directory
- Log files

## State Persistence & Deduplication

The service maintains persistent state across restarts to prevent duplicate emails and respect rate limits:

### What Gets Persisted

1. **Email Send Counts**: Tracks emails sent per URL in the last 24 hours
   - Respects `max_emails_per_url_per_day` limit even after restart
   - Example: If 2 emails sent before restart, won't send 2 more after restart

2. **Seen Matches (Content Hashing)**: Tracks unique incidents for 7 days
   - Each incident is hashed: SHA256(URL + Date + Time + Address)
   - Same outage on same date/time = same hash = no duplicate email
   - Example: If same "2024-10-31, 08:00-16:00, Ulica XYZ" appears after restart, won't email again

3. **Alert Cooldown Times**: Remembers when last alert was sent per URL
   - Prevents rapid-fire alerts even after restart

4. **Error Email Counts**: Tracks connection error emails per URL (max 3/day)

### State File Example

```json
{
  "seen_matches": {
    "a3f4e2...": {
      "first_seen": "2024-10-31T10:30:00Z",
      "last_notified": "2024-10-31T10:30:00Z",
      "count": 1,
      "date": "2024-10-31",
      "time": "08:00-16:00",
      "address": "Ulica Braće Jerković 1-100",
      "url": "https://elektrodistribucija.rs/..."
    }
  },
  "emails_sent_per_url_today": {
    "https://elektrodistribucija.rs/...": [
      "2024-10-31T10:30:00Z",
      "2024-10-31T14:20:00Z"
    ]
  }
}
```

### Automatic State Management

- **Auto-save**: Every 5 minutes
- **Auto-cleanup**: Old data removed (>24h for emails, >7d for matches)
- **Corruption handling**: If state file corrupted, backed up and started fresh
- **Graceful degradation**: If state file missing/unreadable, starts with empty state

## How It Works

The service performs intelligent monitoring and information extraction:

1. **Every 5 minutes** (configurable), checks all configured URLs
2. **Smart search logic** (fully configurable, no hardcoded values, **case-insensitive**):
   - **For 2 search terms**: Term 1 = broad, Term 2 = specific
     - ❌ **Only term 1 found** → Ignore (too broad)
     - ✅ **Term 1 + term 2** → Match (specific area mentioned)
     - ✅ **Only term 2** → Match (specific area mentioned)
   - **Example with "Земун" (broad) + "Батајница" (specific)**:
     - ❌ Only "Земун" → Ignore | ✅ "Земун" + "Батајница" → Match | ✅ Only "Батајница" → Match
   - **Case-insensitive**: `"Батајница"` matches `"БАТАЈНИЦА"`, `"батајница"`, `"Батајница"`, etc.
   - **For 1 or 3+ search terms**: All must be present (standard AND logic)
3. **Per-URL search terms**: Each URL uses its own specific search terms
   - **Power**: "Земун", "Батајница" (municipality + settlement)
   - **Water**: "Земун", "Батајница" (municipality + settlement)
   - **Tip**: You can use any case in config.json - matching is automatic!
4. **Rotating User-Agents**: Fetches recent browser User-Agents on startup and rotates through them (configurable)
   - Automatically fetches from [microlinkhq/top-user-agents](https://github.com/microlinkhq/top-user-agents)
   - Source based on 300M+ monthly requests, updated regularly
   - Configurable pool size (1-100 agents, default: 6)
   - Can be disabled to use static User-Agent
   - Falls back to hardcoded agent if fetch fails
   - Email notification sent to admin on fetch failure
5. **HTML parsing** using `golang.org/x/net/html` for accurate text extraction
6. **Section filtering** for water malfunctions: only extracts from "Без воде су потрошачи" section, ignoring "Распоред аутоцистерни" (cistern trucks)
7. **URL-specific extraction** when search terms are detected:
   - **Power (Elektrodistribucija)**:
     - Date: "Планирана искључења за датум: 01.11.2025."
     - Time: Time range like "08:00-16:00"
     - Address: Detailed street names and neighborhoods
   - **Water Planned (BVK planirani-radovi)**:
     - Date: Date ranges like "31.10/01.11.2025. године"
     - Time: "у времену од 22.00 до 06.00 сати"
     - Location: Settlement names like "у naseljима Batajnica i Busije"
   - **Water Malfunctions (BVK kvarovi)**:
     - Location: Specific streets by municipality "Земун: Street names"
     - Time: Estimated repair time "До 15:00"
8. **Email alert** sent with type-specific formatting (⚡ power, 💧 water)
9. **Smart limiting**: 
   - Global: 20 emails per hour maximum
   - Per-URL matches: **2 emails per day maximum** (prevents spam from same URL)
   - Per-URL errors: **3 error emails per day maximum** (connection failures)
   - Cooldown: 60 minutes between alerts for same URL
10. **Connection monitoring**:
   - Detects when URLs become unreachable
   - Sends error notification to `error_recipient`
   - Sends recovery notification when connection is restored
   - Tracks downtime duration
11. **Tracks matches** in recent events (configurable hours)

## Search Logic Details

### Smart Two-Term Search (Generic, Configurable)

The service implements intelligent search logic to avoid false positives. **This logic is fully generic and driven by your `config.json`** - no hardcoded city names!

#### How It Works

When you configure **exactly 2 search terms** for a URL:
- **Term 1** (first in array) = **Broader/general** term (e.g., municipality, region)
- **Term 2** (second in array) = **Specific** term (e.g., settlement, neighborhood)

The service applies smart filtering to prevent false positives from the broad term alone.

#### Rules (Example: "Земун" + "Батајница")

| Content Found | Action | Reason |
|---------------|--------|---------|
| ❌ **Only term 1** ("Земун") | **IGNORE** | Too broad - could be anywhere in the municipality |
| ✅ **Term 1 + term 2** ("Земун" + "Батајница") | **MATCH** | Valid - specific area is mentioned |
| ✅ **Only term 2** ("Батајница") | **MATCH** | Valid - specific area is mentioned |
| ❌ **Neither** | **IGNORE** | No relevant location found |

**Note:** This works with **any** 2 search terms you configure! Examples: `["Београд", "Нови Београд"]`, `["Сурчин", "Добановци"]`, etc.

#### Example Scenarios

**❌ Scenario 1: Only Zemun (IGNORED)**
```
Content: "Земун: Улица Главна 15, Горња Земун 20"
Search Terms: ["Земун", "Батајница"]
Result: NO MATCH (Батајница not found)
```

**✅ Scenario 2: Zemun + Batajnica (MATCHED)**
```
Content: "Земун: Раде Кончара 20, Батајнички друм бб"
Search Terms: ["Земун", "Батајница"]
Result: MATCH ✓ (Both found, Батајница present)
```

**✅ Scenario 3: Only Batajnica (MATCHED)**
```
Content: "У naseljima Батајница и Бусије"
Search Terms: ["Земун", "Батајница"]
Result: MATCH ✓ (Батајница found)
```

**✅ Scenario 4: Direct Batajnica (MATCHED)**
```
Content: "31.10/01.11.2025. године – у naseljima Батајница и Бусије"
Search Terms: ["Земун", "Батајница"]
Result: MATCH ✓ (Батајница found)
```

**✅ Scenario 5: Case Insensitive (MATCHED)**
```
Content: "Насеље БАТАЈНИЦА: ДРАГЕ МИХАЈЛОВИЋА 60-80"
Search Terms: ["земун", "батајница"]  ← lowercase in config
Result: MATCH ✓ (БАТАЈНИЦА matches батајница, case-insensitive)
```

### Water Malfunctions - Section Filtering

For `https://www.bvk.rs/kvarovi-na-mrezi/`, the service only extracts data from the relevant section:

**✅ Extract From:**
```html
<strong>Без воде су потрошачи у наведеним и околним улицама:</strong>
<ul>
  <li><strong>Земун:</strong> Раде Кончара 20, Првомајска бб</li>
  <li><strong>Батајнички друм бб (Земун)</strong> – 1 возило</li>
</ul>
```

**❌ Do NOT Extract From:**
```html
<strong>Распоред аутоцистерни:</strong>
<ul>
  <li>Батајнички друм бб (Земун) – 1 возило</li>
  <li>...</li>
</ul>
```

**Why?** The cistern truck section shows where **water trucks are parked** (temporary water supply), not where water outages are. We only want actual outage locations from the "Без воде су потрошачи" section.

### Implementation (Generic, No Hardcoded Values, Case-Insensitive)

```go
func containsAllSearchTerms(content string, terms []string) bool {
    if len(terms) == 0 {
        return false
    }
    
    // Convert to lowercase for case-insensitive matching
    contentLower := strings.ToLower(content)
    
    // For exactly 2 search terms: use smart broad/specific logic
    if len(terms) == 2 {
        broadTerm := strings.ToLower(terms[0])    // First term = broader
        specificTerm := strings.ToLower(terms[1]) // Second term = specific
        
        hasBroad := strings.Contains(contentLower, broadTerm)
        hasSpecific := strings.Contains(contentLower, specificTerm)
        
        // Only broad term found → Ignore (too broad)
        if hasBroad && !hasSpecific {
            return false
        }
        
        // Specific term found (with or without broad) → Match
        if hasSpecific {
            return true
        }
        
        return false
    }
    
    // For 1 or 3+ terms: all must be present
    for _, term := range terms {
        termLower := strings.ToLower(term)
        if !strings.Contains(contentLower, termLower) {
            return false
        }
    }
    return true
}
```

**Key Features:**
- ✅ Works with **any** search terms from your `config.json` - no city names are hardcoded!
- ✅ **Case-insensitive** matching - write search terms in any case you prefer
- ✅ `"Батајница"` in config matches `"БАТАЈНИЦА"` in HTML automatically

### Testing

To test the logic manually:

```bash
# Test with only Zemun (should not match)
echo "Земун: Улица Главна 15" | grep -q "Батајница" || echo "IGNORED ✓"

# Test with Zemun + Batajnica (should match)
echo "Земун: Батајнички друм бб" | grep -q "Батајница" && echo "MATCHED ✓"

# Test with only Batajnica (should match)
echo "У naseljима Батајница и Бусије" | grep -q "Батајница" && echo "MATCHED ✓"
```

### Email Formats

#### Power Outage Alert (⚡)
When power outage is detected:

```
Subject: ⚡ Nece biti struje u Batajnici - 01.11.2025.

Nece biti struje u Batajnici:

01.11.2025.

Vreme: 08:00-16:00 h

Na adresama: Naselje Batajnica, ulica Svetog Nikole...
```

#### Water Planned Work Alert (💧)
When water maintenance is scheduled:

```
Subject: 💧 Planirana iskljucenja vode - 31.10/01.11.2025. године

Planirana iskljucenja vode u Batajnici:

31.10/01.11.2025. године

Vreme: у времену од 22.00 до 06.00 сати

Lokacije: у naseljima Батајница и Бусије
```

#### Water Malfunction Alert (💧)
When water service is interrupted:

```
Subject: 💧 KVAR - Nema vode u Batajnici

Trenutno nema vode na sledecim lokacijama:

Земун: Раде Кончара 20, Првомајска бб

Procenjeno vreme popravke: До 15:00

Za vise informacija: https://www.bvk.rs/kvarovi-na-mrezi/
```

#### Connection Error (to error_recipient)

```
Subject: 🔴 Nestanak-Info - Connection Error: Power - Day 1

Connection Error Detected

URL Name: Power - Day 1
URL: https://elektrodistribucija.rs/...

Error Details:
connection timeout / HTTP error / etc.

Timestamp: 2025-10-31 15:30:00

This URL is currently unreachable. You will receive a recovery notification when the connection is restored.
```

#### Connection Recovery (to error_recipient)

```
Subject: 🟢 Nestanak-Info - Connection Restored: Power - Day 1

Connection Restored

URL Name: Power - Day 1
URL: https://elektrodistribucija.rs/...

Downtime Duration: 2h 15m 30s
Restored At: 2025-10-31 17:45:30

The URL is now reachable again and monitoring has resumed.
```

## Email Setup (Brevo)

1. Sign up for a free account at https://www.brevo.com/
2. Get your API key from the dashboard
3. Add the API key to `config.json`
4. Verify your sender email address in Brevo

Free tier includes 300 emails per day, which is plenty for monitoring alerts.

## Use Cases

### Power Outage Monitoring
Monitor electric company websites for scheduled outages in your area.

### Water Outage Monitoring
Track water company announcements for your neighborhood.

### Custom Monitoring
Monitor any website for specific text or announcements.

## Technical Details

- **Language**: Go 1.25+
- **Email Provider**: Brevo (SendInBlue) API via `github.com/sendinblue/APIv3-go-library/v2`
- **HTML Parsing**: `golang.org/x/net/html` for accurate content extraction
- **Authentication**: Argon2id password hashing with `golang.org/x/crypto`
- **Concurrency**: Worker pool for efficient URL checking
- **Security**: Rate limiting, security headers, session management
- **DNS Caching**: 5-minute TTL with automatic cleanup and fallback
- **User-Agent Rotation** (configurable): 
  - Fetches top 100 User-Agents from [microlinkhq/top-user-agents](https://github.com/microlinkhq/top-user-agents) on startup
  - Data based on 300M+ monthly real-world requests
  - Configurable pool size (`user_agent_pool_size`: 1-100, default: 6)
  - Can be disabled (`user_agent_rotation_enabled: false`)
  - Selects diverse agents (Chrome, Firefox, Safari) and rotates through them
  - Automatic fallback to hardcoded agent if fetch fails
  - Email notification to `error_recipient` on fetch failure

## Troubleshooting

### Service won't start
```bash
# Check logs for errors
sudo journalctl -u nestanak-info -n 50

# Verify configuration
sudo /opt/nestanak-info/nestanak-info -check-config
```

### Emails not sending
- Verify Brevo API key is correct
- Check email rate limits in logs
- Ensure sender email is verified in Brevo
- Check Brevo dashboard for delivery status

### Web interface not accessible
- Check `http_listen` in config.json
- Verify firewall rules if accessing remotely
- Check service logs for binding errors

## License

MIT License - Feel free to use and modify as needed.

## Credits

Based on the architecture of ping-monitor service.

