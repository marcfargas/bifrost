package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/spf13/cobra"
)

var sendCmd = &cobra.Command{
	Use:   "send AGENT MESSAGE...",
	Short: "Send a noreply message to an agent",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		to := args[0]
		body := strings.Join(args[1:], " ")
		return sendMessage(to, body)
	},
}

func sendMessage(to, body string) error {
	ctx := context.Background()

	conn, err := tryHubConnect()
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer conn.Close()

	mux := newCLIMux(ctx, conn)

	msg := protocol.Message{
		To:        to,
		From:      "cli",
		Type:      protocol.MessageTypeContext,
		Body:      body,
		Priority:  protocol.PriorityNormal,
		Timestamp: time.Now(),
		NoReply:   true,
	}

	resp, err := mux.rpcCall(ctx, "msg.send", msg)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub error: %s", resp.Error.Message)
	}

	fmt.Printf("Message sent to %s\n", to)
	return nil
}
