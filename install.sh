#!/bin/bash

# Nestanak-Info Service Installer
# This script installs the nestanak-info service as a systemd service

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
SERVICE_NAME="nestanak-info"
SERVICE_USER="nestanak"
INSTALL_DIR="/opt/nestanak-info"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

# Check if this is an update or fresh install
UPDATE_MODE=false
if [ -d "$INSTALL_DIR" ] && [ -f "$SERVICE_FILE" ]; then
    UPDATE_MODE=true
    echo -e "${YELLOW}🔄 Existing installation detected - Running in UPDATE mode${NC}"
else
    echo -e "${GREEN}🚀 Installing Nestanak-Info Service${NC}"
fi
echo "================================================"

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   echo -e "${RED}❌ This script must be run as root${NC}"
   echo "Please run: sudo $0"
   exit 1
fi

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo -e "${YELLOW}⚠️  Go is not installed. Installing Go...${NC}"
    
    # Install Go
    dnf update -y
    dnf install -y golang
    
    # Set GOPATH and add to PATH
    echo 'export GOPATH=$HOME/go' >> /etc/profile
    echo 'export PATH=$PATH:/usr/local/go/bin:$GOPATH/bin' >> /etc/profile
    source /etc/profile
fi

# Create service user (skip if updating)
if [ "$UPDATE_MODE" = false ]; then
    echo -e "${GREEN}👤 Creating service user...${NC}"
    if ! id "$SERVICE_USER" &>/dev/null; then
        useradd -r -s /bin/false -d "$INSTALL_DIR" "$SERVICE_USER"
        echo -e "${GREEN}✅ Service user created: $SERVICE_USER${NC}"
    else
        echo -e "${YELLOW}⚠️  Service user already exists: $SERVICE_USER${NC}"
    fi
else
    echo -e "${GREEN}👤 Service user already exists: $SERVICE_USER${NC}"
fi

# Stop service if updating
if [ "$UPDATE_MODE" = true ]; then
    echo -e "${YELLOW}⏹️  Stopping service for update...${NC}"
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    echo -e "${GREEN}✅ Service stopped${NC}"
fi

# Save existing config to temporary location if updating
TEMP_CONFIG=""
if [ "$UPDATE_MODE" = true ] && [ -f "$INSTALL_DIR/config.json" ]; then
    echo -e "${YELLOW}💾 Preserving existing configuration...${NC}"
    TEMP_CONFIG=$(mktemp)
    cp "$INSTALL_DIR/config.json" "$TEMP_CONFIG"
    echo -e "${GREEN}✅ Configuration preserved${NC}"
fi

# Create installation directory
echo -e "${GREEN}📁 Creating installation directory...${NC}"
mkdir -p "$INSTALL_DIR"
chown "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR"

# Copy files to installation directory
echo -e "${GREEN}📋 Copying service files...${NC}"
cp *.go "$INSTALL_DIR/"
cp go.mod "$INSTALL_DIR/"
cp go.sum "$INSTALL_DIR/" 2>/dev/null || true
cp config.json "$INSTALL_DIR/"

# Copy templates directory (required for HTTP interface)
echo -e "${GREEN}📁 Copying HTML templates...${NC}"
mkdir -p "$INSTALL_DIR/templates"
cp templates/*.html "$INSTALL_DIR/templates/" 2>/dev/null || true

# Restore existing config if this was an update
if [ "$UPDATE_MODE" = true ] && [ -n "$TEMP_CONFIG" ] && [ -f "$TEMP_CONFIG" ]; then
    cp "$TEMP_CONFIG" "$INSTALL_DIR/config.json"
    rm -f "$TEMP_CONFIG"
    echo -e "${GREEN}✅ Existing configuration restored${NC}"
fi

# Create one-time backup of original config for fresh installs only
if [ "$UPDATE_MODE" = false ] && [ ! -f "$INSTALL_DIR/config.json.original" ]; then
    cp "$INSTALL_DIR/config.json" "$INSTALL_DIR/config.json.original"
    echo -e "${GREEN}✅ Original configuration backed up to config.json.original${NC}"
fi

# Set proper ownership (includes all subdirectories like templates/)
chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR"

# Build the service binary
echo -e "${GREEN}🔨 Building service binary...${NC}"
cd "$INSTALL_DIR"

# Install dependencies
echo "Installing Go dependencies..."
su -s /bin/bash -c "cd $INSTALL_DIR && go mod tidy" "$SERVICE_USER"

# Build the binary with optimizations
echo "Building nestanak-info binary (optimized)..."
su -s /bin/bash -c "cd $INSTALL_DIR && go build -ldflags='-s -w' -trimpath -o nestanak-info" "$SERVICE_USER"

# Make binary executable
chmod +x nestanak-info

echo -e "${GREEN}✅ Service binary built successfully${NC}"

# Create network wait script
echo -e "${GREEN}📡 Creating network wait script...${NC}"
cat > "$INSTALL_DIR/wait-for-network.sh" << 'WAITSCRIPT'
#!/bin/bash
# Wait for network interface to be ready
MAX_WAIT=60
WAIT_INTERVAL=2
elapsed=0

echo "Waiting for network interface to be ready..."

# Extract HTTP bind address from config
CONFIG_FILE="$1"
if [ -f "$CONFIG_FILE" ]; then
    # Parse HTTP address from config.json (handles both "ip:port" and ":port")
    HTTP_ADDR=$(grep -oP '"http_listen"\s*:\s*"\K[^"]+' "$CONFIG_FILE" 2>/dev/null || echo "")
    
    if [ -n "$HTTP_ADDR" ]; then
        # Extract IP (everything before last colon, or empty if starts with :)
        BIND_IP="${HTTP_ADDR%:*}"
        
        # If BIND_IP equals HTTP_ADDR, it means there's no colon or it's just ":port"
        if [ "$BIND_IP" = "$HTTP_ADDR" ]; then
            BIND_IP=""
        fi
        
        echo "Config HTTP address: $HTTP_ADDR"
        echo "Extracted bind IP: ${BIND_IP:-0.0.0.0 (all interfaces)}"
        
        # Wait for specific IP to be available (skip if empty or 0.0.0.0)
        if [ -n "$BIND_IP" ] && [ "$BIND_IP" != "0.0.0.0" ] && [ "$BIND_IP" != "::" ]; then
            while [ $elapsed -lt $MAX_WAIT ]; do
                if ip addr show | grep -q "inet $BIND_IP"; then
                    echo "✅ Network interface with $BIND_IP is ready"
                    exit 0
                fi
                echo "⏳ Waiting for $BIND_IP... ($elapsed/$MAX_WAIT seconds)"
                sleep $WAIT_INTERVAL
                elapsed=$((elapsed + WAIT_INTERVAL))
            done
            echo "⚠️  Timeout waiting for $BIND_IP, proceeding anyway..."
        else
            echo "✅ Binding to all interfaces, no wait needed"
        fi
    fi
fi

# General network readiness check
while [ $elapsed -lt $MAX_WAIT ]; do
    if ip route | grep -q default; then
        echo "✅ Default route is ready"
        exit 0
    fi
    echo "⏳ Waiting for network... ($elapsed/$MAX_WAIT seconds)"
    sleep $WAIT_INTERVAL
    elapsed=$((elapsed + WAIT_INTERVAL))
done

echo "⚠️  Timeout waiting for network, proceeding anyway (service will auto-restart if bind fails)"
exit 0
WAITSCRIPT

chmod +x "$INSTALL_DIR/wait-for-network.sh"
chown "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR/wait-for-network.sh"

# Create systemd service file
echo -e "${GREEN}⚙️  Creating systemd service...${NC}"
cat > "$SERVICE_FILE" << EOF
[Unit]
Description=🔌🚰 Nestanak-Info Service - Power & Water Outage Monitor
Documentation=https://github.com/yourusername/nestanak-info
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
WorkingDirectory=$INSTALL_DIR

# Wait for network interface before starting
ExecStartPre=$INSTALL_DIR/wait-for-network.sh $INSTALL_DIR/config.json

ExecStart=$INSTALL_DIR/nestanak-info

# Auto-restart if network wasn't ready (will retry every 15 seconds)
Restart=always
RestartSec=15

StandardOutput=journal
StandardError=journal
SyslogIdentifier=nestanak-info

# Security settings
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$INSTALL_DIR

# Resource limits
LimitNOFILE=65536
MemoryMax=256M

[Install]
WantedBy=multi-user.target
EOF

# Reload systemd and enable service
echo -e "${GREEN}🔄 Reloading systemd and enabling service...${NC}"
systemctl daemon-reload
systemctl enable "$SERVICE_NAME"

echo -e "${GREEN}✅ Service installed and enabled${NC}"

# Create log directory
mkdir -p /var/log/nestanak-info
chown "$SERVICE_USER:$SERVICE_USER" /var/log/nestanak-info

echo ""
if [ "$UPDATE_MODE" = true ]; then
    echo -e "${GREEN}🎉 Update completed successfully!${NC}"
    echo "================================================"
    echo -e "${GREEN}What was updated:${NC}"
    echo "  • Service binary rebuilt with latest code"
    echo "  • Dependencies updated (go.mod, go.sum)"
    echo "  • Configuration preserved (your settings kept)"
    echo ""
    echo -e "${YELLOW}🔄 Starting service...${NC}"
    systemctl start "$SERVICE_NAME"
    sleep 2
    if systemctl is-active --quiet "$SERVICE_NAME"; then
        echo -e "${GREEN}✅ Service started successfully!${NC}"
    else
        echo -e "${RED}❌ Service failed to start. Check logs:${NC}"
        echo "  sudo journalctl -u $SERVICE_NAME -n 50"
    fi
else
    echo -e "${GREEN}🎉 Installation completed successfully!${NC}"
    echo "================================================"
    echo -e "${GREEN}Service Details:${NC}"
    echo "  • Service Name: $SERVICE_NAME"
    echo "  • Install Directory: $INSTALL_DIR"
    echo "  • Service User: $SERVICE_USER"
    echo "  • Config File: $INSTALL_DIR/config.json"
    echo ""
    echo -e "${YELLOW}⚠️  Next Steps:${NC}"
    echo "  1. Update your Brevo API key in: $INSTALL_DIR/config.json"
    echo "  2. Configure your recipients in: $INSTALL_DIR/config.json"
    echo "  3. Configure search terms in: $INSTALL_DIR/config.json"
    echo "  4. Start the service with: sudo systemctl start $SERVICE_NAME"
fi

echo ""
echo -e "${GREEN}Management Commands:${NC}"
echo "  • Start service:    sudo systemctl start $SERVICE_NAME"
echo "  • Stop service:     sudo systemctl stop $SERVICE_NAME"
echo "  • Restart service:  sudo systemctl restart $SERVICE_NAME"
echo "  • Check status:     sudo systemctl status $SERVICE_NAME"
echo "  • View logs:        sudo journalctl -u $SERVICE_NAME -f"
echo ""
echo -e "${GREEN}📝 Configuration:${NC}"
echo "  • Edit config:      sudo nano $INSTALL_DIR/config.json"
echo "  • Original backup:  $INSTALL_DIR/config.json.original"
echo ""
if [ "$UPDATE_MODE" = true ]; then
    echo -e "${GREEN}🔄 To update again in the future:${NC}"
else
    echo -e "${GREEN}🔄 To update in the future:${NC}"
fi
echo "  • Pull latest code from git"
echo "  • Run: sudo ./install.sh"
echo ""
echo -e "${GREEN}🚀 Service is ready!${NC}"

