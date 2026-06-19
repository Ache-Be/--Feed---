package mq_client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const feedPublishQueue = "feed.publish"

var conn *amqp.Connection

type FeedEvent struct {
	AuthorID string `json:"author_id"`
	VideoID  string `json:"video_id"`
	Score    int64  `json:"score"`
}

func Init(_ context.Context) (*amqp.Connection, error) {
	url := envOrDefault("RABBITMQ_URL", "amqp://guest:guest@127.0.0.1:5672/")

	c, err := amqp.Dial(url)
	if err != nil {
		return nil, err
	}

	ch, err := c.Channel()
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	defer ch.Close()

	if _, err := ch.QueueDeclare(feedPublishQueue, true, false, false, false, nil); err != nil {
		_ = c.Close()
		return nil, err
	}

	conn = c
	return conn, nil
}

func Enabled() bool {
	return conn != nil && !conn.IsClosed()
}

func Close() {
	if conn == nil {
		return
	}
	_ = conn.Close()
	conn = nil
}

func PublishFeedEvent(ctx context.Context, event FeedEvent) error {
	if !Enabled() {
		return errors.New("rabbitmq not initialized")
	}

	body, err := json.Marshal(event)
	if err != nil {
		return err
	}

	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	if _, err := ch.QueueDeclare(feedPublishQueue, true, false, false, false, nil); err != nil {
		return err
	}

	pubCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	return ch.PublishWithContext(pubCtx, "", feedPublishQueue, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		Body:         body,
	})
}

func ConsumeFeedEvents(ctx context.Context, handler func(FeedEvent) error) error {
	if !Enabled() {
		return errors.New("rabbitmq not initialized")
	}

	ch, err := conn.Channel()
	if err != nil {
		return err
	}

	if _, err := ch.QueueDeclare(feedPublishQueue, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		return err
	}
	if err := ch.Qos(20, 0, false); err != nil {
		_ = ch.Close()
		return err
	}

	msgs, err := ch.Consume(feedPublishQueue, "", false, false, false, false, nil)
	if err != nil {
		_ = ch.Close()
		return err
	}

	go func() {
		<-ctx.Done()
		_ = ch.Close()
	}()

	for msg := range msgs {
		var event FeedEvent
		if err := json.Unmarshal(msg.Body, &event); err != nil {
			_ = msg.Nack(false, false)
			continue
		}

		if err := handler(event); err != nil {
			_ = msg.Nack(false, true)
			continue
		}

		_ = msg.Ack(false)
	}

	return nil
}

func envOrDefault(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}
