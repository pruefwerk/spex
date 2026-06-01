package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoDBExpectRequest struct {
	URI          string
	Database     string
	Collection   string
	Username     string
	Password     string
	FilterFile   string
	MatchersFile string
	Timeout      time.Duration
	PollInterval time.Duration
}

func expectMongoDB(req mongoDBExpectRequest) error {
	filter, err := readMongoDBFilter(req.FilterFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()

	clientOptions := options.Client().ApplyURI(req.URI)
	if req.Username != "" || req.Password != "" {
		clientOptions.SetAuth(options.Credential{
			Username: req.Username,
			Password: req.Password,
		})
	}
	client, err := mongo.Connect(clientOptions)
	if err != nil {
		return err
	}
	defer client.Disconnect(context.Background())
	if err := client.Ping(ctx, nil); err != nil {
		return err
	}

	collection := client.Database(req.Database).Collection(req.Collection)
	var lastErr error
	for {
		var document bson.M
		err := collection.FindOne(ctx, filter).Decode(&document)
		if err == nil {
			if matchErr := evaluateMongoDBDocument(req.MatchersFile, document); matchErr == nil {
				return nil
			} else {
				lastErr = matchErr
			}
		} else if errors.Is(err, mongo.ErrNoDocuments) {
			lastErr = err
		} else {
			lastErr = err
		}

		timer := time.NewTimer(req.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return fmt.Errorf("mongodb expectation timed out: %w", lastErr)
			}
			return fmt.Errorf("mongodb expectation timed out")
		case <-timer.C:
		}
	}
}

func readMongoDBFilter(path string) (bson.M, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var filter bson.M
	if err := bson.UnmarshalExtJSON(content, true, &filter); err != nil {
		return nil, fmt.Errorf("mongodb filter: %w", err)
	}
	return filter, nil
}

func evaluateMongoDBDocument(matchersFile string, document bson.M) error {
	extended, err := bson.MarshalExtJSON(document, true, false)
	if err != nil {
		return err
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(extended))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("mongodb document: %w", err)
	}
	return EvaluateMatchersFileAgainstDocument(matchersFile, decoded)
}

func mongoDBCredentialsFromEnv() (string, string) {
	return os.Getenv("SPEX_MONGODB_USERNAME"), os.Getenv("SPEX_MONGODB_PASSWORD")
}
