#!/bin/bash
# Quick verification script for opencode-proxy-api

set -e

echo "🧪 OpenCode Proxy API - Quick Test"
echo "=================================="
echo ""

# Check if server is running
if ! curl -s http://localhost:8080/healthz > /dev/null 2>&1; then
    echo "❌ Server not running on :8080"
    echo "   Start with: ./opencode-proxy-linux-amd64"
    exit 1
fi

echo "✅ Server is running"

# Test health endpoint
echo ""
echo "📊 Health Check:"
curl -s http://localhost:8080/ | jq '.' 2>/dev/null || curl -s http://localhost:8080/

# Test with session affinity
echo ""
echo "🔗 Testing Session Affinity..."
SESSION_ID="test-session-$(date +%s)"

echo "   Request 1 (session: $SESSION_ID):"
curl -s http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test" \
  -H "x-session-id: $SESSION_ID" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hi"}],
    "max_tokens": 5
  }' 2>&1 | head -3

echo ""
echo "   Request 2 (same session: $SESSION_ID):"
curl -s http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test" \
  -H "x-session-id: $SESSION_ID" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 5
  }' 2>&1 | head -3

echo ""
echo "   Request 3 (different session):"
curl -s http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test" \
  -H "x-session-id: another-session" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Test"}],
    "max_tokens": 5
  }' 2>&1 | head -3

echo ""
echo "✅ Tests complete"
echo ""
echo "💡 Check server logs to verify:"
echo "   - Requests 1 & 2 used the same proxy (same session)"
echo "   - Request 3 may use a different proxy (different session)"
echo ""
echo "📝 Log pattern to look for:"
echo '   session=test-session-* request_id=req_*'
