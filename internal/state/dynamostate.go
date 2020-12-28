package state

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/cresta/zapctx"
	"go.uber.org/zap"
	"time"
)

type DynamoDBStorage struct {
	TableName string
	Log *zapctx.Logger
	Client *dynamodb.DynamoDB
	SyncCachePrefix string
}

type storageObject struct {
	Key string
	TgARN string
	Hostname string
	State State
}

func (d *DynamoDBStorage) GetStates(ctx context.Context, syncPairs []StateKeys) (map[StateKeys]State, error) {
	toFetch := make([]map[string]*dynamodb.AttributeValue, 0, len(syncPairs))
	for _, sp := range syncPairs {
		toFetch = append(toFetch, map[string]*dynamodb.AttributeValue {
			"Key": {
				S:    aws.String(sp.String()),
			},
		})
	}
	res, err := d.Client.BatchGetItemWithContext(ctx, &dynamodb.BatchGetItemInput{
		RequestItems:           map[string]*dynamodb.KeysAndAttributes{
			d.TableName: {
				Keys:                     toFetch,
			},
		},
	})
	if err != nil {
		d.Log.IfErr(err).Warn(ctx, "unable to fetch items", zap.Any("items", syncPairs))
		return nil, fmt.Errorf("unable to fetch items: %w", err)
	}

	ret := make(map[StateKeys]State, len(syncPairs))
	for idx, vals := range res.Responses[d.TableName] {
		var into storageObject
		if err := dynamodbattribute.UnmarshalMap(vals, &into); err != nil {
			return nil, fmt.Errorf("unable to unmarshal item %d: %w", idx, err)
		}
		ret[StateKeys{
			TargetGroupARN: TargetGroupARN(into.TgARN),
			Hostname: into.Hostname,
		}] = into.State
	}
	// Fill in missing values
	for _, sp := range syncPairs {
		if _, exists := ret[sp]; !exists {
			ret[sp] = State{}
		}
	}
	return ret, nil
}

func (d *DynamoDBStorage) Store(ctx context.Context, toStore map[StateKeys]State) error {
	values, err := makeWriteRequest(toStore)
	if err != nil {
		return fmt.Errorf("unable to create dynamodb write object: %w", err)
	}
	_, err = d.Client.BatchWriteItemWithContext(ctx, &dynamodb.BatchWriteItemInput{
		RequestItems: map[string][]*dynamodb.WriteRequest {
			d.TableName: values,
		},
	})
	if err != nil {
		return fmt.Errorf("unable to write to dynamodb: %w", err)
	}
	return nil
}

func makeWriteRequest(store map[StateKeys]State) ([]*dynamodb.WriteRequest, error) {
	ret := make([]*dynamodb.WriteRequest, 0, len(store))
	for k, v := range store {
		if len(v.Targets) == 0 {
			ret = append(ret, &dynamodb.WriteRequest{
				DeleteRequest:    &dynamodb.DeleteRequest{
					Key: map[string]*dynamodb.AttributeValue{
						"Key": {
							S:    aws.String(k.String()),
						},
					},
				},
			})
			continue
		}
		so := storageObject {
			Key: k.String(),
			TgARN: string(k.TargetGroupARN),
			Hostname:  k.Hostname,
			State: v,
		}
		encoded, err := dynamodbattribute.MarshalMap(so)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal object %s: %w", k, err)
		}
		ret = append(ret, &dynamodb.WriteRequest{
			PutRequest:    &dynamodb.PutRequest{
				Item: encoded,
			},
		})
	}
	return ret, nil
}

type syncCacheObject struct {
	Key string
	ExpireAt time.Time
	cache map[TargetGroupARN]string
}

func (d *DynamoDBStorage) StoreSync(ctx context.Context, toStore map[TargetGroupARN]string, expireAt time.Time) error {
	if toStore == nil {
		_, err := d.Client.DeleteItemWithContext(ctx, &dynamodb.DeleteItemInput{
			Key:                         d.cacheKey(),
			TableName:                   &d.TableName,
		})
		if err != nil {
			return fmt.Errorf("unable to clear cache: %w", err)
		}
		return nil
	}
	dynamodbObject := syncCacheObject{
		Key:   "synccache_" + d.SyncCachePrefix,
		ExpireAt:   expireAt,
		cache: toStore,
	}
	encoded, err := dynamodbattribute.MarshalMap(dynamodbObject)
	if err != nil {
		return fmt.Errorf("unable to encode cache object: %w", err)
	}
	_, err = d.Client.PutItemWithContext(ctx, &dynamodb.PutItemInput{
		Item:                        encoded,
		TableName:                   &d.TableName,
	})
	if err != nil {
		return fmt.Errorf("unable t o write object to cache: %w", err)
	}
	return nil
}

func (d *DynamoDBStorage) cacheKey() map[string]*dynamodb.AttributeValue {
	return map[string]*dynamodb.AttributeValue{
		"Key": {
			S:    aws.String("synccache_" + d.SyncCachePrefix),
		},
	}
}

func (d *DynamoDBStorage) GetSync(ctx context.Context, currentTime time.Time) (map[TargetGroupARN]string, error) {
	out, err := d.Client.GetItemWithContext(ctx, &dynamodb.GetItemInput{
		Key: d.cacheKey(),
		TableName:                &d.TableName,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to get cached tag state: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var cachedObject syncCacheObject
	if err := dynamodbattribute.UnmarshalMap(out.Item, &cachedObject); err != nil {
		return nil, fmt.Errorf("unable to unmarshal map: %w", err)
	}
	if cachedObject.ExpireAt.Before(currentTime) {
		return nil, nil
	}
	return cachedObject.cache, nil
}

var _ Storage = &DynamoDBStorage{}

var _ SyncCache = &DynamoDBStorage{}