package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/bytedance/sonic"
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/message"
)

const (
	defaultTableName = "goat_contexts"
	defaultRegion    = "us-east-1"
)

// Config configures the DynamoDB context store.
type Config struct {
	TableName string
	Region    string

	// Optional: provide AWS config directly
	AWSConfig *aws.Config

	// Optional: AWS credentials (if AWSConfig is nil)
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSSessionToken    string

	// AutoCreateTable creates the table if it doesn't exist (default: true)
	// Use nil for default behavior (auto-create), false to disable, true to enable explicitly
	AutoCreateTable bool

	// ReadCapacityUnits for provisioned mode (0 = on-demand)
	ReadCapacityUnits int64
	// WriteCapacityUnits for provisioned mode (0 = on-demand)
	WriteCapacityUnits int64
}

// DynamoDBStore persists context heads and messages in a single DynamoDB table.
type DynamoDBStore struct {
	client    *dynamodb.Client
	tableName string
}

var _ contextmgr.Store = (*DynamoDBStore)(nil)

// NewDynamoDBStore creates a new DynamoDB store with automatic table creation.
func NewDynamoDBStore(ctx context.Context, cfg Config) (*DynamoDBStore, error) {
	tableName := cfg.TableName
	if tableName == "" {
		tableName = defaultTableName
	}

	// Load AWS config
	awsConfig := cfg.AWSConfig
	if awsConfig == nil {
		region := cfg.Region
		if region == "" {
			region = defaultRegion
		}

		configOpts := []func(*config.LoadOptions) error{
			config.WithRegion(region),
		}

		// If explicit credentials are provided, use them
		if cfg.AWSAccessKeyID != "" && cfg.AWSSecretAccessKey != "" {
			configOpts = append(configOpts, config.WithCredentialsProvider(
				aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
					return aws.Credentials{
						AccessKeyID:     cfg.AWSAccessKeyID,
						SecretAccessKey: cfg.AWSSecretAccessKey,
						SessionToken:    cfg.AWSSessionToken,
						Source:          "StaticCredentials",
					}, nil
				}),
			))
		}

		loadedConfig, err := config.LoadDefaultConfig(ctx, configOpts...)
		if err != nil {
			return nil, fmt.Errorf("load AWS config: %w", err)
		}
		awsConfig = &loadedConfig
	}

	client := dynamodb.NewFromConfig(*awsConfig)

	store := &DynamoDBStore{
		client:    client,
		tableName: tableName,
	}

	if cfg.AutoCreateTable {
		if err := store.ensureTable(ctx, cfg); err != nil {
			return nil, fmt.Errorf("ensure DynamoDB table: %w", err)
		}
	}

	return store, nil
}

// NewDynamoDBStoreWithClient wraps an existing DynamoDB client.
func NewDynamoDBStoreWithClient(client *dynamodb.Client, tableName string) *DynamoDBStore {
	if tableName == "" {
		tableName = defaultTableName
	}
	return &DynamoDBStore{
		client:    client,
		tableName: tableName,
	}
}

// ensureTable creates the table if it doesn't exist.
func (s *DynamoDBStore) ensureTable(ctx context.Context, cfg Config) error {
	// Check if table exists
	_, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(s.tableName),
	})

	if err == nil {
		// Table exists
		return nil
	}

	// Check if it's a "table not found" error
	var notFoundErr *types.ResourceNotFoundException
	if !errors.As(err, &notFoundErr) {
		return fmt.Errorf("describe table: %w", err)
	}

	// Table doesn't exist, create it
	billingMode := types.BillingModePayPerRequest
	var provisionedThroughput *types.ProvisionedThroughput

	if cfg.ReadCapacityUnits > 0 || cfg.WriteCapacityUnits > 0 {
		billingMode = types.BillingModeProvisioned
		provisionedThroughput = &types.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(cfg.ReadCapacityUnits),
			WriteCapacityUnits: aws.Int64(cfg.WriteCapacityUnits),
		}
	}

	createInput := &dynamodb.CreateTableInput{
		TableName:                 aws.String(s.tableName),
		BillingMode:               billingMode,
		DeletionProtectionEnabled: aws.Bool(true),
		AttributeDefinitions: []types.AttributeDefinition{
			{
				AttributeName: aws.String("pk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("sk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
		},
		KeySchema: []types.KeySchemaElement{
			{
				AttributeName: aws.String("pk"),
				KeyType:       types.KeyTypeHash,
			},
			{
				AttributeName: aws.String("sk"),
				KeyType:       types.KeyTypeRange,
			},
		},
		ProvisionedThroughput: provisionedThroughput,
	}

	_, err = s.client.CreateTable(ctx, createInput)
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Wait for table to be active (timeout: 5 minutes)
	waiter := dynamodb.NewTableExistsWaiter(s.client)
	err = waiter.Wait(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(s.tableName),
	}, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("wait for table: %w", err)
	}

	return nil
}

// NewDynamoDBContextManager creates a Manager backed by DynamoDB.
func NewDynamoDBContextManager(ctx context.Context, config Config) (*contextmgr.Manager, error) {
	store, err := NewDynamoDBStore(ctx, config)
	if err != nil {
		return nil, err
	}
	return contextmgr.NewManager(store), nil
}

func (s *DynamoDBStore) LoadHead(ctx context.Context, uid common.ContextUID) (*contextmgr.Head, error) {
	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: headPK(uid)},
			"sk": &types.AttributeValueMemberS{Value: "META"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get head: %w", err)
	}

	if result.Item == nil {
		return nil, contextmgr.ErrContextNotFound
	}

	var item HeadItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return nil, fmt.Errorf("unmarshal head: %w", err)
	}

	return headItemToHead(&item), nil
}

func (s *DynamoDBStore) CommitAppend(ctx context.Context, rows []contextmgr.MessageRow, next *contextmgr.Head, expectVersion uint64) error {
	if next == nil {
		return errors.New("commit append requires a head")
	}

	// Build transaction items
	transactItems := []types.TransactWriteItem{}

	// 1. Put or update head with version check
	headItem := headToHeadItem(next)
	headAttrs, err := attributevalue.MarshalMap(headItem)
	if err != nil {
		return fmt.Errorf("marshal head: %w", err)
	}

	if expectVersion == 0 {
		// Create new context - use condition to prevent overwrite
		transactItems = append(transactItems, types.TransactWriteItem{
			Put: &types.Put{
				TableName:           aws.String(s.tableName),
				Item:                headAttrs,
				ConditionExpression: aws.String("attribute_not_exists(pk)"),
			},
		})
	} else {
		// Update existing context with version check
		transactItems = append(transactItems, types.TransactWriteItem{
			Put: &types.Put{
				TableName:           aws.String(s.tableName),
				Item:                headAttrs,
				ConditionExpression: aws.String("version = :expected_version"),
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":expected_version": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", expectVersion)},
				},
			},
		})
	}

	// 2. Put all message rows
	for _, row := range rows {
		msgItem, err := messageRowToItem(row)
		if err != nil {
			return err
		}

		msgAttrs, err := attributevalue.MarshalMap(msgItem)
		if err != nil {
			return fmt.Errorf("marshal message: %w", err)
		}

		transactItems = append(transactItems, types.TransactWriteItem{
			Put: &types.Put{
				TableName: aws.String(s.tableName),
				Item:      msgAttrs,
			},
		})
	}

	// Execute transaction
	_, err = s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: transactItems,
	})

	if err != nil {
		// Check for condition check failure
		var txCanceledErr *types.TransactionCanceledException
		if errors.As(err, &txCanceledErr) {
			if len(txCanceledErr.CancellationReasons) > 0 {
				reason := txCanceledErr.CancellationReasons[0]
				if reason.Code != nil && *reason.Code == "ConditionalCheckFailed" {
					if expectVersion == 0 {
						return contextmgr.ErrCASConflict // Context already exists
					}
					// Check if context exists
					_, loadErr := s.LoadHead(ctx, next.UID)
					if loadErr != nil {
						if errors.Is(loadErr, contextmgr.ErrContextNotFound) {
							return contextmgr.ErrContextNotFound
						}
						return loadErr
					}
					return contextmgr.ErrCASConflict
				}
			}
		}
		return fmt.Errorf("transaction failed: %w", err)
	}

	return nil
}

func (s *DynamoDBStore) ReadMessages(ctx context.Context, uid common.ContextUID, lane contextmgr.Lane, fromSeq, toSeq uint64) ([]contextmgr.MessageRow, error) {
	if fromSeq == 0 || toSeq < fromSeq {
		return []contextmgr.MessageRow{}, nil
	}

	pk := messagePK(uid, lane)
	fromSK := messageSK(fromSeq)
	toSK := messageSK(toSeq)

	result, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("pk = :pk AND sk BETWEEN :from_sk AND :to_sk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":      &types.AttributeValueMemberS{Value: pk},
			":from_sk": &types.AttributeValueMemberS{Value: fromSK},
			":to_sk":   &types.AttributeValueMemberS{Value: toSK},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}

	rows := make([]contextmgr.MessageRow, 0, len(result.Items))
	for _, item := range result.Items {
		var msgItem MessageItem
		if err := attributevalue.UnmarshalMap(item, &msgItem); err != nil {
			return nil, fmt.Errorf("unmarshal message: %w", err)
		}

		row, err := itemToMessageRow(&msgItem)
		if err != nil {
			return nil, err
		}
		rows = append(rows, *row)
	}

	return rows, nil
}

func (s *DynamoDBStore) ReplaceCommitted(ctx context.Context, uid common.ContextUID, rows []contextmgr.MessageRow, next *contextmgr.Head, expectVersion uint64) error {
	if next == nil || next.UID != uid {
		return errors.New("replacement head UID does not match context UID")
	}

	// DynamoDB transactions have a 100-item limit
	// Strategy: Use batched non-transactional operations for messages, then update head with version check
	// This trades full atomicity for the ability to handle large replacements

	pk := messagePK(uid, contextmgr.LaneCommitted)

	// 1. Query all existing committed messages with pagination
	var allOldKeys []map[string]types.AttributeValue
	var lastEvaluatedKey map[string]types.AttributeValue

	for {
		queryInput := &dynamodb.QueryInput{
			TableName:              aws.String(s.tableName),
			KeyConditionExpression: aws.String("pk = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": &types.AttributeValueMemberS{Value: pk},
			},
			ProjectionExpression: aws.String("pk, sk"),
			Limit:                aws.Int32(100),
		}
		if lastEvaluatedKey != nil {
			queryInput.ExclusiveStartKey = lastEvaluatedKey
		}

		queryResult, err := s.client.Query(ctx, queryInput)
		if err != nil {
			return fmt.Errorf("query existing messages: %w", err)
		}

		allOldKeys = append(allOldKeys, queryResult.Items...)

		if queryResult.LastEvaluatedKey == nil {
			break
		}
		lastEvaluatedKey = queryResult.LastEvaluatedKey
	}

	// 2. Delete old messages in batches of 25 (BatchWriteItem limit)
	for i := 0; i < len(allOldKeys); i += 25 {
		end := i + 25
		if end > len(allOldKeys) {
			end = len(allOldKeys)
		}

		batch := allOldKeys[i:end]
		writeRequests := make([]types.WriteRequest, len(batch))
		for j, key := range batch {
			writeRequests[j] = types.WriteRequest{
				DeleteRequest: &types.DeleteRequest{Key: key},
			}
		}

		_, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]types.WriteRequest{
				s.tableName: writeRequests,
			},
		})
		if err != nil {
			return fmt.Errorf("batch delete old messages: %w", err)
		}
	}

	// 3. Insert new committed messages in batches of 25
	for i := 0; i < len(rows); i += 25 {
		end := i + 25
		if end > len(rows) {
			end = len(rows)
		}

		batch := rows[i:end]
		writeRequests := make([]types.WriteRequest, len(batch))

		for j, row := range batch {
			msgItem, err := messageRowToItem(row)
			if err != nil {
				return err
			}

			msgAttrs, err := attributevalue.MarshalMap(msgItem)
			if err != nil {
				return fmt.Errorf("marshal message: %w", err)
			}

			writeRequests[j] = types.WriteRequest{
				PutRequest: &types.PutRequest{Item: msgAttrs},
			}
		}

		_, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]types.WriteRequest{
				s.tableName: writeRequests,
			},
		})
		if err != nil {
			return fmt.Errorf("batch write new messages: %w", err)
		}
	}

	// 4. Update head with version check (provides atomicity guarantee)
	headItem := headToHeadItem(next)
	headAttrs, err := attributevalue.MarshalMap(headItem)
	if err != nil {
		return fmt.Errorf("marshal head: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                headAttrs,
		ConditionExpression: aws.String("version = :expected_version"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":expected_version": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", expectVersion)},
		},
	})

	if err != nil {
		var condCheckErr *types.ConditionalCheckFailedException
		if errors.As(err, &condCheckErr) {
			// Check if context exists
			_, loadErr := s.LoadHead(ctx, uid)
			if loadErr != nil {
				if errors.Is(loadErr, contextmgr.ErrContextNotFound) {
					return contextmgr.ErrContextNotFound
				}
				return loadErr
			}
			return contextmgr.ErrCASConflict
		}
		return fmt.Errorf("update head failed: %w", err)
	}

	return nil
}

func (s *DynamoDBStore) DeleteContext(ctx context.Context, uid common.ContextUID) error {
	// Query all items for this context
	// We need to delete both HEAD and all messages

	// 1. Delete head
	headPk := headPK(uid)
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: headPk},
			"sk": &types.AttributeValueMemberS{Value: "META"},
		},
	})
	if err != nil {
		return fmt.Errorf("delete head: %w", err)
	}

	// 2. Delete all messages (both committed and pending lanes)
	for _, lane := range []contextmgr.Lane{contextmgr.LaneCommitted, contextmgr.LanePending} {
		pk := messagePK(uid, lane)

		// Query messages
		queryResult, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.tableName),
			KeyConditionExpression: aws.String("pk = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": &types.AttributeValueMemberS{Value: pk},
			},
			ProjectionExpression: aws.String("pk, sk"),
		})
		if err != nil {
			return fmt.Errorf("query messages for deletion: %w", err)
		}

		// Delete in batches of 25 (BatchWriteItem limit)
		for i := 0; i < len(queryResult.Items); i += 25 {
			end := i + 25
			if end > len(queryResult.Items) {
				end = len(queryResult.Items)
			}

			batch := queryResult.Items[i:end]
			writeRequests := make([]types.WriteRequest, len(batch))
			for j, item := range batch {
				writeRequests[j] = types.WriteRequest{
					DeleteRequest: &types.DeleteRequest{
						Key: item,
					},
				}
			}

			_, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
				RequestItems: map[string][]types.WriteRequest{
					s.tableName: writeRequests,
				},
			})
			if err != nil {
				return fmt.Errorf("batch delete messages: %w", err)
			}
		}
	}

	return nil
}

// messageRowToItem converts a MessageRow to a DynamoDB item.
func messageRowToItem(row contextmgr.MessageRow) (*MessageItem, error) {
	msgBytes, err := sonic.Marshal(row.Message)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}

	return &MessageItem{
		PK:      messagePK(row.UID, row.Lane),
		SK:      messageSK(row.Seq),
		UID:     string(row.UID),
		Lane:    string(row.Lane),
		Seq:     row.Seq,
		Message: msgBytes,
		Type:    "MESSAGE",
	}, nil
}

// itemToMessageRow converts a DynamoDB item to a MessageRow.
func itemToMessageRow(item *MessageItem) (*contextmgr.MessageRow, error) {
	var msg message.Message
	if err := sonic.Unmarshal(item.Message, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	return &contextmgr.MessageRow{
		UID:     common.ContextUID(item.UID),
		Lane:    contextmgr.Lane(item.Lane),
		Seq:     item.Seq,
		Message: &msg,
	}, nil
}
