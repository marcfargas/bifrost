// fakesender registers with a bifrost hub and sends a message after a delay.
// Usage: fakesender <socket-path> <target-agent> <delay-seconds> <message>
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintf(os.Stderr, "usage: fakesender <socket> <target> <delay-sec> <message>\n")
		os.Exit(1)
	}
	socketPath := os.Args[1]
	target := os.Args[2]
	delaySec, _ := strconv.Atoi(os.Args[3])
	message := os.Args[4]

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	// Register
	enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "hub.register",
		"params": map[string]any{
			"agent_id": "fake-sender", "username": "ci", "hostname": "ci",
			"local_path": "/tmp/fake", "project_name": "fake-sender",
			"display_name": "ci@fake-sender", "protocol_version": "1.0.0",
			"connected_at": time.Now().Format(time.RFC3339),
			"last_seen":    time.Now().Format(time.RFC3339),
		},
	})

	// Read response (and any notifications)
	var resp json.RawMessage
	dec.Decode(&resp)
	fmt.Fprintf(os.Stderr, "registered\n")

	// Wait
	time.Sleep(time.Duration(delaySec) * time.Second)

	// Send message. If target is "*", broadcast. Otherwise prefix with "agent:".
	to := target
	if to != "*" {
		to = "agent:" + target
	}
	enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "msg.send",
		"params": map[string]any{
			"from": "fake-sender", "to": to,
			"type": "QUESTION", "body": message, "priority": "normal",
		},
	})

	dec.Decode(&resp)
	fmt.Fprintf(os.Stderr, "sent: %s\n", string(resp))
}
