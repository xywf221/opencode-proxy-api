#!/bin/bash
# Test session affinity: same conversation should use same proxy

set -e

API_URL="${API_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"

echo "Testing session affinity with conversation tracking..."
echo "API: $API_URL"
echo

# Test 1: Same conversation ID should use same session
echo "=== Test 1: Explicit conversation ID ==="
for i in {1..3}; do
  echo "Request $i with conversation_id=conv123..."
  curl -s "$API_URL/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -H "x-conversation-id: conv123" \
    -d '{
      "model": "deepseek-v4-flash-free",
      "messages": [{"role": "user", "content": "Hello"}],
      "stream": false
    }' | jq -r '.id' || echo "Failed"
  sleep 1
done

echo
echo "=== Test 2: Same message content (should hash to same session) ==="
for i in {1..3}; do
  echo "Request $i with same message..."
  curl -s "$API_URL/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -d '{
      "model": "deepseek-v4-flash-free",
      "messages": [{"role": "user", "content": "What is 2+2?"}],
      "stream": false
    }' | jq -r '.id' || echo "Failed"
  sleep 1
done

echo
echo "=== Test 3: Different message content (should use different sessions) ==="
for i in {1..3}; do
  echo "Request $i with unique message..."
  curl -s "$API_URL/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -d "{
      \"model\": \"deepseek-v4-flash-free\",
      \"messages\": [{\"role\": \"user\", \"content\": \"Message $i\"}],
      \"stream\": false
    }" | jq -r '.id' || echo "Failed"
  sleep 1
done

echo
echo "Check server logs for session IDs - same conversation should have same session ID"
