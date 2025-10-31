#!/bin/bash

# Nestanak-Info Service Uninstaller
# This script removes the nestanak-info service from the system

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

echo -e "${YELLOW}⚠️  Nestanak-Info Service Uninstaller${NC}"
echo "================================================"
echo ""
echo -e "${YELLOW}This will remove:${NC}"
echo "  • Service: $SERVICE_NAME"
echo "  • User: $SERVICE_USER"
echo "  • Directory: $INSTALL_DIR"
echo "  • Config: $INSTALL_DIR/config.json"
echo ""
read -p "Are you sure you want to uninstall? (yes/no): " CONFIRM

if [ "$CONFIRM" != "yes" ]; then
    echo -e "${GREEN}Uninstall cancelled.${NC}"
    exit 0
fi

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   echo -e "${RED}❌ This script must be run as root${NC}"
   echo "Please run: sudo $0"
   exit 1
fi

echo ""
echo -e "${YELLOW}Starting uninstallation...${NC}"

# Stop and disable service
if systemctl is-active --quiet "$SERVICE_NAME"; then
    echo -e "${YELLOW}⏹️  Stopping service...${NC}"
    systemctl stop "$SERVICE_NAME"
    echo -e "${GREEN}✅ Service stopped${NC}"
fi

if systemctl is-enabled --quiet "$SERVICE_NAME" 2>/dev/null; then
    echo -e "${YELLOW}🔄 Disabling service...${NC}"
    systemctl disable "$SERVICE_NAME"
    echo -e "${GREEN}✅ Service disabled${NC}"
fi

# Remove systemd service file
if [ -f "$SERVICE_FILE" ]; then
    echo -e "${YELLOW}🗑️  Removing systemd service file...${NC}"
    rm -f "$SERVICE_FILE"
    systemctl daemon-reload
    echo -e "${GREEN}✅ Service file removed${NC}"
fi

# Remove installation directory
if [ -d "$INSTALL_DIR" ]; then
    echo -e "${YELLOW}🗑️  Removing installation directory...${NC}"
    rm -rf "$INSTALL_DIR"
    echo -e "${GREEN}✅ Installation directory removed${NC}"
fi

# Remove service user
if id "$SERVICE_USER" &>/dev/null; then
    echo -e "${YELLOW}👤 Removing service user...${NC}"
    userdel "$SERVICE_USER" 2>/dev/null || true
    echo -e "${GREEN}✅ Service user removed${NC}"
fi

# Remove log directory
if [ -d "/var/log/nestanak-info" ]; then
    echo -e "${YELLOW}🗑️  Removing log directory...${NC}"
    rm -rf /var/log/nestanak-info
    echo -e "${GREEN}✅ Log directory removed${NC}"
fi

echo ""
echo -e "${GREEN}🎉 Uninstallation completed successfully!${NC}"
echo "================================================"
echo -e "${GREEN}Nestanak-Info service has been completely removed from the system.${NC}"

