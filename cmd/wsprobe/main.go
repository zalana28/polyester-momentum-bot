// Command wsprobe diagnoses the realtime WebSocket handshake against the
// Polyester testnet endpoint. It prints the HTTP status returned by the
// server for the upgrade request.
package main

import (
	"fmt"
	"os"

	"github.com/gorilla/websocket"

	polyester "github.com/Fabric-Labs/polyester-sdk-go"
)

func main() {
	url := "wss://api.testnet.polyester.com"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	if env := os.Getenv("POLYESTER_ENV"); env == "" && len(os.Args) == 1 {
		_ = polyester.TestnetEnvironment // reference pins
	}
	dialer := websocket.Dialer{Subprotocols: []string{"centrifuge-protobuf"}}
	conn, resp, err := dialer.Dial(url, nil)
	if err != nil {
		fmt.Printf("dial error: %v\n", err)
		if resp != nil {
			fmt.Printf("status: %s\n", resp.Status)
			for k, v := range resp.Header {
				fmt.Printf("%s: %v\n", k, v)
			}
			buf := make([]byte, 512)
			n, _ := resp.Body.Read(buf)
			if n > 0 {
				fmt.Printf("body: %s\n", string(buf[:min(n, 300)]))
			}
		}
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Println("HANDSHAKE OK:", resp.Status, "subprotocol:", conn.Subprotocol())
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
