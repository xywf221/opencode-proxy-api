#!/bin/bash
# Test script to reproduce the tool_call_id bug

API_URL="http://localhost:8080"
API_KEY="${OPCODE_API_KEY:-test-key}"

echo "=== Test 1: Valid tool_result with tool_use_id ==="
curl -s -X POST "$API_URL/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v4-flash-free",
    "messages": [
      {
        "role": "user",
        "content": "Use the add function to calculate 2+3"
      },
      {
        "role": "assistant",
        "content": [
          {
            "type": "tool_use",
            "id": "call_123",
            "name": "add",
            "input": {"a": 2, "b": 3}
          }
        ]
      },
      {
        "role": "user",
        "content": [
          {
            "type": "tool_result",
            "tool_use_id": "call_123",
            "content": "5"
          }
        ]
      }
    ],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "add",
          "description": "Add two numbers",
          "parameters": {
            "type": "object",
            "properties": {
              "a": {"type": "number"},
              "b": {"type": "number"}
            }
          }
        }
      }
    ]
  }' | jq -r '.error.message // "Success"'

echo ""
echo "=== Test 2: Invalid tool_result WITHOUT tool_use_id (should be skipped) ==="
curl -s -X POST "$API_URL/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v4-flash-free",
    "messages": [
      {
        "role": "user",
        "content": "Use the add function"
      },
      {
        "role": "assistant",
        "content": [
          {
            "type": "tool_use",
            "id": "call_456",
            "name": "add",
            "input": {"a": 10, "b": 20}
          }
        ]
      },
      {
        "role": "user",
        "content": [
          {
            "type": "tool_result",
            "content": "Result without ID - should be skipped"
          },
          {
            "type": "tool_result",
            "tool_use_id": "call_456",
            "content": "30"
          }
        ]
      }
    ],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "add",
          "parameters": {"type": "object"}
        }
      }
    ]
  }' | jq -r '.error.message // "Success"'

echo ""
echo "=== Test 3: Check what request body is actually sent ==="
# Enable debug mode to see the transformed request
curl -s -X POST "$API_URL/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v4-flash-free",
    "messages": [
      {
        "role": "assistant",
        "content": [{"type": "tool_use", "id": "x", "name": "t", "input": {}}]
      },
      {
        "role": "user",
        "content": [{"type": "tool_result", "content": "no ID"}]
      }
    ]
  }' 2>&1 | head -20
