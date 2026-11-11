package proxy

import (
	"fmt"
	"kiro-go/config"
	"strings"
)

type kiroWire int

const (
	kiroWireRuntimeRest kiroWire = iota
	kiroWireQRest
	kiroWireQRPC
	kiroWireResponses
)

type kiroRequestConfig struct {
	URL          string
	Origin       string
	ContentType  string
	AmzTarget    string
	HeaderValues func(account *config.Account, host string) kiroHeaderValues
	Wire         kiroWire
}

func resolveKiroRequestConfig(account *config.Account, profileArn string) kiroRequestConfig {
	region := kiroRegionForProfile(account, profileArn)
	choice := ""
	if account != nil {
		choice = strings.ToLower(strings.TrimSpace(account.KiroEndpoint))
	}
	if choice == "" {
		if cfg := config.Get(); cfg != nil && cfg.QEndpoint {
			choice = "q-rest"
		} else {
			choice = "runtime"
		}
	}

	switch choice {
	case "q":
		return kiroRequestConfig{
			URL:          fmt.Sprintf("https://q.%s.amazonaws.com/", region),
			Origin:       "AI_EDITOR",
			ContentType:  "application/x-amz-json-1.0",
			AmzTarget:    "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
			HeaderValues: buildGenerateHeaderValues,
			Wire:         kiroWireQRPC,
		}
	case "q-rest":
		return kiroRequestConfig{
			URL:          fmt.Sprintf("https://q.%s.amazonaws.com/generateAssistantResponse", region),
			Origin:       "AI_EDITOR",
			ContentType:  "application/json",
			HeaderValues: buildGenerateHeaderValues,
			Wire:         kiroWireQRest,
		}
	case "responses":
		return kiroRequestConfig{
			URL:         fmt.Sprintf("https://runtime.%s.kiro.dev/v1/responses", region),
			Origin:      "AI_EDITOR",
			Wire:        kiroWireResponses,
			ContentType: "application/json",
		}
	default:
		return kiroRequestConfig{
			URL:          fmt.Sprintf("https://runtime.%s.kiro.dev/generateAssistantResponse", region),
			Origin:       "AI_EDITOR",
			ContentType:  "application/json",
			HeaderValues: buildGenerateHeaderValues,
			Wire:         kiroWireRuntimeRest,
		}
	}
}
