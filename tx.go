package dynamo

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/dynamodb"
)

type getTxOp interface {
	getTxItem() (*dynamodb.TransactGetItem, error)
}

// GetTx is a transaction to retrieve items.
// It can contain up to 10 operations and works across multiple tables.
// GetTx is analogous to TransactGetItems in DynamoDB's API.
// See: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactGetItems.html
type GetTx struct {
	db           *DB
	items        []getTxOp
	unmarshalers map[getTxOp]interface{}
	cc           *ConsumedCapacity
}

// GetTransaction begins a new get transaction.
func (db *DB) GetTx() *GetTx {
	return &GetTx{
		db: db,
	}
}

// Get adds a get request to this transaction.
func (tx *GetTx) Get(q *Query) *GetTx {
	tx.items = append(tx.items, q)
	return tx
}

// GetOne adds a get request to this transaction, and specifies out to which the results are marshaled.
// Out must be a pointer. You can use this multiple times in one transaction.
func (tx *GetTx) GetOne(q *Query, out interface{}) *GetTx {
	if tx.unmarshalers == nil {
		tx.unmarshalers = make(map[getTxOp]interface{})
	}
	tx.items = append(tx.items, q)
	tx.unmarshalers[q] = out
	return tx
}

// ConsumedCapacity will measure the throughput capacity consumed by this transaction and add it to cc.
func (tx *GetTx) ConsumedCapacity(cc *ConsumedCapacity) *GetTx {
	tx.cc = cc
	return tx
}

// Run executes this transaction and unmarshals everything specified by GetOne.
func (tx *GetTx) Run() error {
	ctx, cancel := defaultContext()
	defer cancel()
	return tx.RunWithContext(ctx)
}

// RunWithContext executes this transaction and unmarshals everything specified by GetOne.
func (tx *GetTx) RunWithContext(ctx aws.Context) error {
	input, err := tx.input()
	if err != nil {
		return err
	}
	var resp *dynamodb.TransactGetItemsOutput
	err = retry(ctx, func() error {
		var err error
		resp, err = tx.db.client.TransactGetItems(input)
		if tx.cc != nil && resp != nil {
			for _, cc := range resp.ConsumedCapacity {
				addConsumedCapacity(tx.cc, cc)
			}
		}
		return err
	})
	if err != nil {
		return err
	}
	if isResponsesEmpty(resp.Responses) {
		return ErrNotFound
	}
	return tx.unmarshal(resp)
}

func (tx *GetTx) unmarshal(resp *dynamodb.TransactGetItemsOutput) error {
	for i, item := range resp.Responses {
		if item.Item == nil {
			continue
		}
		if target := tx.unmarshalers[tx.items[i]]; target != nil {
			if err := UnmarshalItem(item.Item, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// All executes this transaction and unmarshals every value to out, which must be a pointer to a slice.
func (tx *GetTx) All(out interface{}) error {
	ctx, cancel := defaultContext()
	defer cancel()
	return tx.AllWithContext(ctx, out)
}

// AllWithContext executes this transaction and unmarshals every value to out, which must be a pointer to a slice.
func (tx *GetTx) AllWithContext(ctx aws.Context, out interface{}) error {
	input, err := tx.input()
	if err != nil {
		return err
	}
	var resp *dynamodb.TransactGetItemsOutput
	err = retry(ctx, func() error {
		var err error
		resp, err = tx.db.client.TransactGetItems(input)
		if tx.cc != nil && resp != nil {
			for _, cc := range resp.ConsumedCapacity {
				addConsumedCapacity(tx.cc, cc)
			}
		}
		return err
	})
	if err != nil {
		return err
	}
	if isResponsesEmpty(resp.Responses) {
		return ErrNotFound
	}
	if err := tx.unmarshal(resp); err != nil {
		return err
	}
	for _, item := range resp.Responses {
		if item.Item == nil {
			continue
		}
		if err := unmarshalAppend(item.Item, out); err != nil {
			return err
		}
	}
	return nil
}

func (tx *GetTx) input() (*dynamodb.TransactGetItemsInput, error) {
	input := &dynamodb.TransactGetItemsInput{}
	for _, item := range tx.items {
		tgi, err := item.getTxItem()
		if err != nil {
			return nil, err
		}
		input.TransactItems = append(input.TransactItems, tgi)
	}
	if tx.cc != nil {
		input.ReturnConsumedCapacity = aws.String(dynamodb.ReturnConsumedCapacityIndexes)
	}
	return input, nil
}

type writeTxOp interface {
	writeTxItem() (*dynamodb.TransactWriteItem, error)
}

// WriteTx is a transaction to delete, put, update, and check items.
// It can contain up to 10 operations and works across multiple tables.
// Two operations cannot target the same item.
// WriteTx is analogous to TransactWriteItems in DynamoDB's API.
// See: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactWriteItems.html
type WriteTx struct {
	db    *DB
	items []writeTxOp
	cc    *ConsumedCapacity
}

// WriteTx begins a new write transaction.
func (db *DB) WriteTx() *WriteTx {
	return &WriteTx{
		db: db,
	}
}

// Delete adds a new delete operation to this transaction.
func (tx *WriteTx) Delete(d *Delete) *WriteTx {
	tx.items = append(tx.items, d)
	return tx
}

// Put adds a put operation to this transaction.
func (tx *WriteTx) Put(p *Put) *WriteTx {
	tx.items = append(tx.items, p)
	return tx
}

// Update adds an update operation to this transaction.
func (tx *WriteTx) Update(u *Update) *WriteTx {
	tx.items = append(tx.items, u)
	return tx
}

// Check adds a conditional check to this transaction.
func (tx *WriteTx) Check(check *ConditionCheck) *WriteTx {
	tx.items = append(tx.items, check)
	return tx
}

// ConsumedCapacity will measure the throughput capacity consumed by this transaction and add it to cc.
func (tx *WriteTx) ConsumedCapacity(cc *ConsumedCapacity) *WriteTx {
	tx.cc = cc
	return tx
}

// Run executes this transaction.
func (tx *WriteTx) Run() error {
	ctx, cancel := defaultContext()
	defer cancel()
	return tx.RunWithContext(ctx)
}

// RunWithContext executes this transaction.
func (tx *WriteTx) RunWithContext(ctx aws.Context) error {
	input, err := tx.input()
	if err != nil {
		return err
	}
	err = retry(ctx, func() error {
		out, err := tx.db.client.TransactWriteItems(input)
		if tx.cc != nil && out != nil {
			for _, cc := range out.ConsumedCapacity {
				addConsumedCapacity(tx.cc, cc)
			}
		}
		return err
	})
	return err
}

func (tx *WriteTx) input() (*dynamodb.TransactWriteItemsInput, error) {
	input := &dynamodb.TransactWriteItemsInput{}
	for _, item := range tx.items {
		wti, err := item.writeTxItem()
		if err != nil {
			return nil, err
		}
		input.TransactItems = append(input.TransactItems, wti)
	}
	if tx.cc != nil {
		input.ReturnConsumedCapacity = aws.String(dynamodb.ReturnConsumedCapacityIndexes)
	}
	return input, nil
}

func isResponsesEmpty(resps []*dynamodb.ItemResponse) bool {
	for _, resp := range resps {
		if resp.Item != nil {
			return false
		}
	}
	return true
}
