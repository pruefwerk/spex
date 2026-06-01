package probe

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type rabbitMQPublishRequest struct {
	URI        string
	Exchange   string
	RoutingKey string
	Username   string
	Password   string
	Payload    []byte
	Timeout    time.Duration
}

type rabbitMQExpectRequest struct {
	URI          string
	Queue        string
	Username     string
	Password     string
	MatchersFile string
	Timeout      time.Duration
	PollInterval time.Duration
}

func publishRabbitMQ(req rabbitMQPublishRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()
	conn, err := amqp.Dial(rabbitMQURI(req.URI, req.Username, req.Password))
	if err != nil {
		return err
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		return err
	}
	defer channel.Close()
	return channel.PublishWithContext(ctx, req.Exchange, req.RoutingKey, false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        req.Payload,
	})
}

func expectRabbitMQ(req rabbitMQExpectRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()
	conn, err := amqp.Dial(rabbitMQURI(req.URI, req.Username, req.Password))
	if err != nil {
		return err
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		return err
	}
	defer channel.Close()

	var lastErr error
	for {
		delivery, ok, err := channel.Get(req.Queue, false)
		if err != nil {
			lastErr = err
		} else if ok {
			if matchErr := EvaluateMatchersBytes(req.MatchersFile, delivery.Body); matchErr == nil {
				if err := delivery.Ack(false); err != nil {
					return err
				}
				return nil
			} else {
				lastErr = matchErr
				if err := delivery.Ack(false); err != nil {
					return err
				}
			}
		} else {
			lastErr = fmt.Errorf("queue %q had no messages", req.Queue)
		}

		timer := time.NewTimer(req.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return fmt.Errorf("rabbitmq expectation timed out: %w", lastErr)
			}
			return fmt.Errorf("rabbitmq expectation timed out")
		case <-timer.C:
		}
	}
}

func rabbitMQURI(raw, username, password string) string {
	if username == "" && password == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = url.UserPassword(username, password)
	return parsed.String()
}

func rabbitMQCredentialsFromEnv() (string, string) {
	return os.Getenv("SPEX_RABBITMQ_USERNAME"), os.Getenv("SPEX_RABBITMQ_PASSWORD")
}
