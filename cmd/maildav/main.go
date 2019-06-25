package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/targodan/maildav"

	"github.com/tarent/logrus"
	"github.com/targodan/go-errors"
	"gopkg.in/urfave/cli.v1"
)

func main() {
	app := cli.NewApp()
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "config,c",
			Usage: "Path to config file.",
			Value: "config.yml",
		},
	}
	app.Action = func(c *cli.Context) error {
		cfgFile, err := os.OpenFile(c.String("config"), os.O_RDONLY, 0644)
		if err != nil {
			return errors.Wrap("Could not open config file", err)
		}

		cfg, err := maildav.ParseConfig(cfgFile)
		cfgFile.Close()
		if err != nil {
			return errors.Wrap("Error parsing config", err)
		}
		// TODO: Actually support multiple pollers
		poller, err := maildav.NewPoller(cfg.Pollers[0])
		if err != nil {
			return errors.Wrap("Error initializing poller", err)
		}

		uploader := &maildav.Uploader{}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt)

		go func() {
			<-sigChan
			cancel()
		}()

		return poller.StartPolling(ctx, uploader)
	}

	err := app.Run(os.Args)
	if err != nil {
		logrus.Fatal(err)
	}
}
