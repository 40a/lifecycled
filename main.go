package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	log "github.com/Sirupsen/logrus"
	"github.com/alecthomas/kingpin"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
)

var (
	Version string
)

const (
	metadataURLInstanceID = "http://169.254.169.254/latest/meta-data/instance-id"
)

func main() {
	log.SetFormatter(&log.TextFormatter{})

	app := kingpin.New("lifecycled",
		"Handle AWS autoscaling lifecycle events gracefully")

	app.Version(Version)
	app.Writer(os.Stdout)
	app.DefaultEnvars()

	var (
		instanceID string
		snsTopic   string
		handler    *os.File
		debug      bool
	)

	app.Flag("instance-id", "The instance id to listen for events for").
		StringVar(&instanceID)

	app.Flag("sns-topic", "The SNS topic that receives events").
		Required().
		StringVar(&snsTopic)

	app.Flag("handler", "The script to invoke to handle events").
		FileVar(&handler)

	app.Flag("debug", "Show debugging info").
		BoolVar(&debug)

	app.Action(func(c *kingpin.ParseContext) error {
		if debug {
			log.SetLevel(log.DebugLevel)
		}

		if instanceID == "" {
			log.Infof("Looking up instance id from metadata service")
			id, err := getInstanceID()
			if err != nil {
				log.Fatalf("Failed to lookup instance id: %v", err)
			}
			instanceID = id
		}

		sess := session.New()
		queue, err := CreateQueue(sess, generateQueueName(instanceID), snsTopic)
		if err != nil {
			log.Fatal(err)
		}

		var cleanup sync.Once
		cleanupFunc := func() {
			queue.Delete()
		}

		defer cleanup.Do(cleanupFunc)

		sigs := make(chan os.Signal, 2)
		signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigs
			log.Info("Shutting down gracefully...")
			cleanup.Do(cleanupFunc)
			os.Exit(1)
		}()

		daemon := Daemon{
			InstanceID:  instanceID,
			AutoScaling: autoscaling.New(sess),
			Handler:     handler,
			Signals:     sigs,
			Queue:       queue,
		}

		return daemon.Start()
	})

	kingpin.MustParse(app.Parse(os.Args[1:]))
}

func getInstanceID() (string, error) {
	res, err := http.Get(metadataURLInstanceID)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Got a %d response from metatadata service", res.StatusCode)
	}

	id, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return string(id), nil
}

func generateQueueName(instanceID string) string {
	return fmt.Sprintf("lifecycled-%s", instanceID)
}
