package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/digitalocean/godo"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/rs/zerolog"
)

func main() {
	config, err := ReadConfig("config.yml")
	if err != nil {
		panic(err)
	}

	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	zerolog.DefaultContextLogger = &logger

	cc, err := githubapp.NewDefaultCachingClientCreator(
		config.Github,
		githubapp.WithClientUserAgent("app-platform-review-apps/1.0.0"),
		githubapp.WithClientTimeout(3*time.Second),
	)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create client creator")
	}

	do := godo.NewFromToken(config.DigitalOcean.Token)

	webhookHandler := githubapp.NewEventDispatcher([]githubapp.EventHandler{
		&PRHandler{cc: cc, do: do},
	}, config.Github.App.WebhookSecret, githubapp.WithScheduler(githubapp.AsyncScheduler()))

	http.Handle("/", webhookHandler)

	addr := fmt.Sprintf("%s:%d", config.Server.Address, config.Server.Port)
	logger.Info().Msgf("Starting server on %s...", addr)
	err = http.ListenAndServe(addr, nil)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to run server")
	}
}
