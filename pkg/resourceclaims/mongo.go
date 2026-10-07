package resourceclaims

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

// MongoStore coordinates independent runners through one atomic document per
// runtime-selected namespace. It never installs TTL indexes or expires owners.
// The host supplies a dedicated collection and authenticated client; submitted
// scenarios must not choose the database, collection, namespace or credentials.
type MongoStore struct {
	collection *mongo.Collection
	namespace  string
}

type mongoDocument struct {
	ID       string `bson:"_id"`
	Schema   string `bson:"schema"`
	Revision string `bson:"revision"`
	State    []byte `bson:"state"`
}

func NewMongoStore(collection *mongo.Collection, namespace string) (*MongoStore, error) {
	if collection == nil || namespace == "" || len(namespace) > 256 || strings.ContainsAny(namespace, "\x00\r\n") {
		return nil, ErrStore
	}
	configured := collection.Clone(options.Collection().SetReadConcern(readconcern.Majority()).SetWriteConcern(writeconcern.Majority()).SetReadPreference(readpref.Primary()))
	return &MongoStore{configured, namespace}, nil
}

func decodeStoredState(data []byte) (State, error) {
	var state State
	if len(data) > 8<<20 {
		return state, ErrStore
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, ErrStore
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || validate(state) != nil {
		return State{}, ErrStore
	}
	return state, nil
}

// Transactions may retry the callback after a compare-and-swap conflict. Callbacks
// must therefore be deterministic, perform no I/O and publish no external effects.
func (s *MongoStore) Transaction(ctx context.Context, transaction func(*State) error) error {
	for attempt := 0; attempt < 100; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var previous mongoDocument
		err := s.collection.FindOne(ctx, bson.M{"_id": s.namespace}).Decode(&previous)
		newDocument := errors.Is(err, mongo.ErrNoDocuments)
		if err != nil && !newDocument {
			return ErrStore
		}
		state := State{}
		if !newDocument {
			if previous.Schema != "spex.resourceclaims/v1" || previous.Revision == "" {
				return ErrStore
			}
			state, err = decodeStoredState(previous.State)
			if err != nil {
				return err
			}
		}
		if err := transaction(&state); err != nil {
			return err
		}
		if validate(state) != nil {
			return ErrStore
		}
		data, err := json.Marshal(state)
		if err != nil || len(data) > 8<<20 {
			return ErrStore
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return ErrStore
		}
		next := mongoDocument{s.namespace, "spex.resourceclaims/v1", hex.EncodeToString(nonce[:]), data}
		if newDocument {
			_, err = s.collection.InsertOne(ctx, next)
			if err == nil {
				return nil
			}
			if !mongo.IsDuplicateKeyError(err) {
				return ErrStore
			}
		} else {
			result, err := s.collection.ReplaceOne(ctx, bson.M{"_id": s.namespace, "revision": previous.Revision}, next)
			if err != nil {
				return ErrStore
			}
			if result.MatchedCount == 1 {
				return nil
			}
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ErrStore
}
