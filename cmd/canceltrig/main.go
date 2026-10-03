package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	polyester "github.com/Fabric-Labs/polyester-sdk-go"
)

func main() {
	raw, _ := os.ReadFile("/Users/jazeerahusna/.zcode/workspace/default/polyester-momentum-bot/.env")
	var kid, priv string
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "API_KEY_ID=") { kid = strings.TrimPrefix(l, "API_KEY_ID=") }
		if strings.HasPrefix(l, "API_PRIVATE_KEY=") { priv = strings.TrimPrefix(l, "API_PRIVATE_KEY=") }
	}
	client, err := polyester.New(polyester.Config{Environment: &polyester.TestnetEnvironment, APIKeyID: kid, APIPrivateKey: priv, HydrateCatalogs: true})
	if err != nil { fmt.Println(err); os.Exit(1) }
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_ = client.WaitForCatalogs(ctx)
	_, err = client.Triggers.Cancel(ctx, nil, "SHvJfFzWLTk", nil)
	fmt.Println("cancel SL:", err)
}
