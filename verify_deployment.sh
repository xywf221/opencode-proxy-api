#!/bin/bash
# Deployment Verification Script

echo "🔍 OpenCode Proxy API - Deployment Verification"
echo "================================================"
echo ""

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check binary exists
echo "1. Checking binary..."
if [ -f "opencode-proxy-linux-amd64" ]; then
    echo -e "${GREEN}✓${NC} Binary exists"
else
    echo -e "${RED}✗${NC} Binary not found"
    exit 1
fi

# Check environment variables
echo ""
echo "2. Checking environment..."
if [ -z "$OPCODE_TOKEN" ]; then
    echo -e "${YELLOW}⚠${NC} OPCODE_TOKEN not set"
else
    echo -e "${GREEN}✓${NC} OPCODE_TOKEN configured"
fi

if [ -n "$OPCODE_PROXY" ]; then
    echo -e "${GREEN}✓${NC} OPCODE_PROXY configured"
elif [ -n "$OPCODE_PROXY_POOL" ]; then
    echo -e "${GREEN}✓${NC} OPCODE_PROXY_POOL configured"
else
    echo -e "${YELLOW}⚠${NC} No proxy configured (will use direct)"
fi

# Check tests
echo ""
echo "3. Running tests..."
go test ./... > /dev/null 2>&1
if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓${NC} All tests pass"
else
    echo -e "${RED}✗${NC} Tests failed"
    exit 1
fi

# Verify key files
echo ""
echo "4. Verifying implementation..."
FILES=(
    "config/proxy_pool.go"
    "internal/session/session.go"
    "internal/retry/retry.go"
    "internal/proxy/handler.go"
)
for file in "${FILES[@]}"; do
    if [ -f "$file" ]; then
        echo -e "${GREEN}✓${NC} $file"
    else
        echo -e "${RED}✗${NC} $file missing"
    fi
done

# Check documentation
echo ""
echo "5. Checking documentation..."
DOCS=("README.md" "IMPROVEMENTS.md" "CHANGELOG.md" "DEPLOYMENT.md")
for doc in "${DOCS[@]}"; do
    if [ -f "$doc" ]; then
        echo -e "${GREEN}✓${NC} $doc"
    else
        echo -e "${YELLOW}⚠${NC} $doc missing"
    fi
done

echo ""
echo "================================================"
echo -e "${GREEN}✓ Deployment verification complete!${NC}"
echo ""
echo "📝 Next steps:"
echo "   1. Set OPCODE_TOKEN in your environment"
echo "   2. Configure proxy: export OPCODE_PROXY=socks5h://user:pass@host:port"
echo "   3. Start server: ./opencode-proxy-linux-amd64"
echo "   4. Test: bash test_quick.sh"
echo ""
