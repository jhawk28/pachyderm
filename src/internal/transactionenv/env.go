package transactionenv

import (
	"context"

	"github.com/gogo/protobuf/proto"

	"github.com/pachyderm/pachyderm/v2/src/auth"
	"github.com/pachyderm/pachyderm/v2/src/client"
	col "github.com/pachyderm/pachyderm/v2/src/internal/collection"
	"github.com/pachyderm/pachyderm/v2/src/internal/serviceenv"
	"github.com/pachyderm/pachyderm/v2/src/pfs"
	"github.com/pachyderm/pachyderm/v2/src/pps"
	"github.com/pachyderm/pachyderm/v2/src/transaction"
)

// PfsWrites is an interface providing a wrapper for each operation that
// may be appended to a transaction through PFS.  Each call may either
// directly run the request through PFS or append it to the active transaction,
// depending on if there is an active transaction in the client context.
type PfsWrites interface {
	CreateRepo(*pfs.CreateRepoRequest) error
	DeleteRepo(*pfs.DeleteRepoRequest) error

	StartCommit(*pfs.StartCommitRequest, *pfs.Commit) (*pfs.Commit, error)
	FinishCommit(*pfs.FinishCommitRequest) error
	SquashCommit(*pfs.SquashCommitRequest) error

	CreateBranch(*pfs.CreateBranchRequest) error
	DeleteBranch(*pfs.DeleteBranchRequest) error
}

// PpsWrites is an interface providing a wrapper for each operation that
// may be appended to a transaction through PPS.  Each call may either
// directly run the request through PPS or append it to the active transaction,
// depending on if there is an active transaction in the client context.
type PpsWrites interface {
	UpdateJobState(*pps.UpdateJobStateRequest) error
	CreatePipeline(*pps.CreatePipelineRequest, **pfs.Commit) error
}

// AuthWrites is an interface providing a wrapper for each operation that
// may be appended to a transaction through the Auth server.  Each call may
// either directly run the request through Auth or append it to the active
// transaction, depending on if there is an active transaction in the client
// context.
type AuthWrites interface {
	ModifyRoleBinding(*auth.ModifyRoleBindingRequest) (*auth.ModifyRoleBindingResponse, error)
	DeleteRoleBinding(*auth.Resource) error
}

// PfsPropagater is the interface that PFS implements to propagate commits at
// the end of a transaction.  It is defined here to avoid a circular dependency.
type PfsPropagater interface {
	PropagateCommit(branch *pfs.Branch, isNewCommit bool) error
	Run() error
}

// PipelineCommitFinisher is an interface to facilitate finishing pipeline commits
// at the end of a transaction
type PipelineCommitFinisher interface {
	FinishPipelineCommits(branch *pfs.Branch) error
	Run() error
}

// TransactionContext is a helper type to encapsulate the state for a given
// set of operations being performed in the Pachyderm API.  When a new
// transaction is started, a context will be created for it containing these
// objects, which will be threaded through to every API call:
//   ctx: the client context which initiated the operations being performed
//   pachClient: the APIClient associated with the client context ctx
//   stm: the object that controls transactionality with etcd.  This is to ensure
//     that all reads and writes are consistent until changes are committed.
//   txnEnv: a struct containing references to each API server, it can be used
//     to make calls to other API servers (e.g. checking auth permissions)
//   pfsDefer: an interface for ensuring certain PFS cleanup tasks are performed
//     properly (and deduped) at the end of the transaction.
type TransactionContext struct {
	ClientContext  context.Context
	Client         *client.APIClient
	Stm            col.STM
	pfsPropagater  PfsPropagater
	commitFinisher PipelineCommitFinisher
	txnEnv         *TransactionEnv
}

// Auth returns a reference to the Auth API Server so that transactionally-
// supported methods can be called across the API boundary without using RPCs
// (which will not maintain transactional guarantees)
func (t *TransactionContext) Auth() AuthTransactionServer {
	return t.txnEnv.authServer
}

// Pfs returns a reference to the PFS API Server so that transactionally-
// supported methods can be called across the API boundary without using RPCs
// (which will not maintain transactional guarantees)
func (t *TransactionContext) Pfs() PfsTransactionServer {
	return t.txnEnv.pfsServer
}

// PropagateCommit saves a branch to be propagated at the end of the transaction
// (if all operations complete successfully).  This is used to batch together
// propagations and dedupe downstream commits in PFS.
func (t *TransactionContext) PropagateCommit(branch *pfs.Branch, isNewCommit bool) error {
	return t.pfsPropagater.PropagateCommit(branch, isNewCommit)
}

func (t *TransactionContext) finish() error {
	if t.commitFinisher != nil {
		if err := t.commitFinisher.Run(); err != nil {
			return err
		}
	}
	if t.pfsPropagater != nil {
		return t.pfsPropagater.Run()
	}
	return nil
}

// FinishPipelineCommits saves a pipeline output branch to have its commits
// finished at the end of the transaction
func (t *TransactionContext) FinishPipelineCommits(branch *pfs.Branch) error {
	if t.commitFinisher != nil {
		return t.commitFinisher.FinishPipelineCommits(branch)
	}
	return nil
}

// TransactionServer is an interface used by other servers to append a request
// to an existing transaction.
type TransactionServer interface {
	AppendRequest(
		context.Context,
		*transaction.Transaction,
		*transaction.TransactionRequest,
	) (*transaction.TransactionResponse, error)
}

// AuthTransactionServer is an interface for the transactionally-supported
// methods that can be called through the auth server.
type AuthTransactionServer interface {
	AuthorizeInTransaction(*TransactionContext, *auth.AuthorizeRequest) (*auth.AuthorizeResponse, error)

	ModifyRoleBindingInTransaction(*TransactionContext, *auth.ModifyRoleBindingRequest) (*auth.ModifyRoleBindingResponse, error)
	GetRoleBindingInTransaction(*TransactionContext, *auth.GetRoleBindingRequest) (*auth.GetRoleBindingResponse, error)

	// Methods to add and remove pipelines from input and output repos. These do their own auth checks
	// for specific permissions required to use a repo as a pipeline input/output.
	AddPipelineReaderToRepoInTransaction(*TransactionContext, string, string) error
	AddPipelineWriterToRepoInTransaction(*TransactionContext, string) error
	RemovePipelineReaderFromRepoInTransaction(*TransactionContext, string, string) error

	// Create and Delete are internal-only APIs used by other services when creating/destroying resources.
	CreateRoleBindingInTransaction(*TransactionContext, string, []string, *auth.Resource) error
	DeleteRoleBindingInTransaction(*TransactionContext, *auth.Resource) error

	// GetPipelineAuthTokenInTransaction is an internal API used by PPS to generate tokens for pipelines
	GetPipelineAuthTokenInTransaction(*TransactionContext, string) (string, error)
	RevokeAuthTokenInTransaction(*TransactionContext, *auth.RevokeAuthTokenRequest) (*auth.RevokeAuthTokenResponse, error)
}

// PfsTransactionServer is an interface for the transactionally-supported
// methods that can be called through the PFS server.
type PfsTransactionServer interface {
	NewPropagater(col.STM) PfsPropagater
	NewPipelineFinisher(*TransactionContext) PipelineCommitFinisher

	CreateRepoInTransaction(*TransactionContext, *pfs.CreateRepoRequest) error
	InspectRepoInTransaction(*TransactionContext, *pfs.InspectRepoRequest) (*pfs.RepoInfo, error)
	DeleteRepoInTransaction(*TransactionContext, *pfs.DeleteRepoRequest) error

	StartCommitInTransaction(*TransactionContext, *pfs.StartCommitRequest, *pfs.Commit) (*pfs.Commit, error)
	FinishCommitInTransaction(*TransactionContext, *pfs.FinishCommitRequest) error
	SquashCommitInTransaction(*TransactionContext, *pfs.SquashCommitRequest) error
	InspectCommitInTransaction(*TransactionContext, *pfs.InspectCommitRequest) (*pfs.CommitInfo, error)

	CreateBranchInTransaction(*TransactionContext, *pfs.CreateBranchRequest) error
	InspectBranchInTransaction(*TransactionContext, *pfs.InspectBranchRequest) (*pfs.BranchInfo, error)
	DeleteBranchInTransaction(*TransactionContext, *pfs.DeleteBranchRequest) error
}

// PpsTransactionServer is an interface for the transactionally-supported
// methods that can be called through the PPS server.
type PpsTransactionServer interface {
	UpdateJobStateInTransaction(*TransactionContext, *pps.UpdateJobStateRequest) error
	CreatePipelineInTransaction(*TransactionContext, *pps.CreatePipelineRequest, **pfs.Commit) error
}

// TransactionEnv contains the APIServer instances for each subsystem that may
// be involved in running transactions so that they can make calls to each other
// without leaving the context of a transaction.  This is a separate object
// because there are cyclic dependencies between APIServer instances.
type TransactionEnv struct {
	serviceEnv serviceenv.ServiceEnv
	txnServer  TransactionServer
	authServer AuthTransactionServer
	pfsServer  PfsTransactionServer
	ppsServer  PpsTransactionServer
}

// Initialize stores the references to APIServer instances in the TransactionEnv
func (env *TransactionEnv) Initialize(
	serviceEnv serviceenv.ServiceEnv,
	txnServer TransactionServer,
	authServer AuthTransactionServer,
	pfsServer PfsTransactionServer,
	ppsServer PpsTransactionServer,
) {
	env.serviceEnv = serviceEnv
	env.txnServer = txnServer
	env.authServer = authServer
	env.pfsServer = pfsServer
	env.ppsServer = ppsServer
}

// Transaction is an interface to unify the code that may either perform an
// action directly or append an action to an existing transaction (depending on
// if there is an active transaction in the client context metadata).  There
// are two implementations of this interface:
//  directTransaction: all operations will be run directly through the relevant
//    server, all inside the same STM.
//  appendTransaction: all operations will be appended to the active transaction
//    which will then be dryrun so that the response for the operation can be
//    returned.  Each operation that is appended will do a new dryrun, so this
//    isn't as efficient as it could be.
type Transaction interface {
	PfsWrites
	PpsWrites
	AuthWrites
}

type directTransaction struct {
	txnCtx *TransactionContext
}

// NewDirectTransaction is a helper function to instantiate a directTransaction
// object.  It is exposed so that the transaction API server can run a direct
// transaction even though there is an active transaction in the context (which
// is why it cannot use `WithTransaction`).
func NewDirectTransaction(txnCtx *TransactionContext) Transaction {
	return &directTransaction{txnCtx: txnCtx}
}

func (t *directTransaction) CreateRepo(original *pfs.CreateRepoRequest) error {
	req := proto.Clone(original).(*pfs.CreateRepoRequest)
	return t.txnCtx.txnEnv.pfsServer.CreateRepoInTransaction(t.txnCtx, req)
}

func (t *directTransaction) DeleteRepo(original *pfs.DeleteRepoRequest) error {
	req := proto.Clone(original).(*pfs.DeleteRepoRequest)
	return t.txnCtx.txnEnv.pfsServer.DeleteRepoInTransaction(t.txnCtx, req)
}

func (t *directTransaction) StartCommit(original *pfs.StartCommitRequest, commit *pfs.Commit) (*pfs.Commit, error) {
	req := proto.Clone(original).(*pfs.StartCommitRequest)
	return t.txnCtx.txnEnv.pfsServer.StartCommitInTransaction(t.txnCtx, req, commit)
}

func (t *directTransaction) FinishCommit(original *pfs.FinishCommitRequest) error {
	req := proto.Clone(original).(*pfs.FinishCommitRequest)
	return t.txnCtx.txnEnv.pfsServer.FinishCommitInTransaction(t.txnCtx, req)
}

func (t *directTransaction) SquashCommit(original *pfs.SquashCommitRequest) error {
	req := proto.Clone(original).(*pfs.SquashCommitRequest)
	return t.txnCtx.txnEnv.pfsServer.SquashCommitInTransaction(t.txnCtx, req)
}

func (t *directTransaction) CreateBranch(original *pfs.CreateBranchRequest) error {
	req := proto.Clone(original).(*pfs.CreateBranchRequest)
	return t.txnCtx.txnEnv.pfsServer.CreateBranchInTransaction(t.txnCtx, req)
}

func (t *directTransaction) DeleteBranch(original *pfs.DeleteBranchRequest) error {
	req := proto.Clone(original).(*pfs.DeleteBranchRequest)
	return t.txnCtx.txnEnv.pfsServer.DeleteBranchInTransaction(t.txnCtx, req)
}

func (t *directTransaction) UpdateJobState(original *pps.UpdateJobStateRequest) error {
	req := proto.Clone(original).(*pps.UpdateJobStateRequest)
	return t.txnCtx.txnEnv.ppsServer.UpdateJobStateInTransaction(t.txnCtx, req)
}

func (t *directTransaction) ModifyRoleBinding(original *auth.ModifyRoleBindingRequest) (*auth.ModifyRoleBindingResponse, error) {
	req := proto.Clone(original).(*auth.ModifyRoleBindingRequest)
	return t.txnCtx.txnEnv.authServer.ModifyRoleBindingInTransaction(t.txnCtx, req)
}

func (t *directTransaction) CreatePipeline(original *pps.CreatePipelineRequest, specCommit **pfs.Commit) error {
	req := proto.Clone(original).(*pps.CreatePipelineRequest)
	return t.txnCtx.txnEnv.ppsServer.CreatePipelineInTransaction(t.txnCtx, req, specCommit)
}

func (t *directTransaction) DeleteRoleBinding(original *auth.Resource) error {
	req := proto.Clone(original).(*auth.Resource)
	return t.txnCtx.txnEnv.authServer.DeleteRoleBindingInTransaction(t.txnCtx, req)
}

type appendTransaction struct {
	ctx       context.Context
	activeTxn *transaction.Transaction
	txnEnv    *TransactionEnv
}

func newAppendTransaction(ctx context.Context, activeTxn *transaction.Transaction, txnEnv *TransactionEnv) Transaction {
	return &appendTransaction{
		ctx:       ctx,
		activeTxn: activeTxn,
		txnEnv:    txnEnv,
	}
}

func (t *appendTransaction) CreateRepo(req *pfs.CreateRepoRequest) error {
	_, err := t.txnEnv.txnServer.AppendRequest(t.ctx, t.activeTxn, &transaction.TransactionRequest{CreateRepo: req})
	return err
}

func (t *appendTransaction) DeleteRepo(req *pfs.DeleteRepoRequest) error {
	_, err := t.txnEnv.txnServer.AppendRequest(t.ctx, t.activeTxn, &transaction.TransactionRequest{DeleteRepo: req})
	return err
}

func (t *appendTransaction) StartCommit(req *pfs.StartCommitRequest, _ *pfs.Commit) (*pfs.Commit, error) {
	res, err := t.txnEnv.txnServer.AppendRequest(t.ctx, t.activeTxn, &transaction.TransactionRequest{StartCommit: req})
	if err != nil {
		return nil, err
	}
	return res.Commit, nil
}

func (t *appendTransaction) FinishCommit(req *pfs.FinishCommitRequest) error {
	_, err := t.txnEnv.txnServer.AppendRequest(t.ctx, t.activeTxn, &transaction.TransactionRequest{FinishCommit: req})
	return err
}

func (t *appendTransaction) SquashCommit(req *pfs.SquashCommitRequest) error {
	_, err := t.txnEnv.txnServer.AppendRequest(t.ctx, t.activeTxn, &transaction.TransactionRequest{SquashCommit: req})
	return err
}

func (t *appendTransaction) CreateBranch(req *pfs.CreateBranchRequest) error {
	_, err := t.txnEnv.txnServer.AppendRequest(t.ctx, t.activeTxn, &transaction.TransactionRequest{CreateBranch: req})
	return err
}

func (t *appendTransaction) DeleteBranch(req *pfs.DeleteBranchRequest) error {
	_, err := t.txnEnv.txnServer.AppendRequest(t.ctx, t.activeTxn, &transaction.TransactionRequest{DeleteBranch: req})
	return err
}

func (t *appendTransaction) UpdateJobState(req *pps.UpdateJobStateRequest) error {
	_, err := t.txnEnv.txnServer.AppendRequest(t.ctx, t.activeTxn, &transaction.TransactionRequest{UpdateJobState: req})
	return err
}

func (t *appendTransaction) CreatePipeline(req *pps.CreatePipelineRequest, _ **pfs.Commit) error {
	_, err := t.txnEnv.txnServer.AppendRequest(t.ctx, t.activeTxn, &transaction.TransactionRequest{CreatePipeline: req})
	return err
}

func (t *appendTransaction) ModifyRoleBinding(original *auth.ModifyRoleBindingRequest) (*auth.ModifyRoleBindingResponse, error) {
	panic("ModifyRoleBinding not yet implemented in transactions")
}

func (t *appendTransaction) DeleteRoleBinding(original *auth.Resource) error {
	panic("DeleteRoleBinding not yet implemented in transactions")
}

// WithTransaction will call the given callback with a txnenv.Transaction
// object, which is instantiated differently based on if an active
// transaction is present in the RPC context.  If an active transaction is
// present, any calls into the Transaction are first dry-run then appended
// to the transaction.  If there is no active transaction, the request will be
// run directly through the selected server.
func (env *TransactionEnv) WithTransaction(ctx context.Context, cb func(Transaction) error) error {
	activeTxn, err := client.GetTransaction(ctx)
	if err != nil {
		return err
	}

	if activeTxn != nil {
		appendTxn := newAppendTransaction(ctx, activeTxn, env)
		return cb(appendTxn)
	}

	return env.WithWriteContext(ctx, func(txnCtx *TransactionContext) error {
		directTxn := NewDirectTransaction(txnCtx)
		return cb(directTxn)
	})
}

// WithWriteContext will call the given callback with a TransactionContext
// which can be used to perform reads and writes on the current cluster state.
func (env *TransactionEnv) WithWriteContext(ctx context.Context, cb func(*TransactionContext) error) error {
	_, err := col.NewSTM(ctx, env.serviceEnv.GetEtcdClient(), func(stm col.STM) error {
		pachClient := env.serviceEnv.GetPachClient(ctx)
		txnCtx := &TransactionContext{
			Client:        pachClient,
			ClientContext: pachClient.Ctx(),
			Stm:           stm,
			txnEnv:        env,
		}
		if env.pfsServer != nil {
			txnCtx.pfsPropagater = env.pfsServer.NewPropagater(stm)
			txnCtx.commitFinisher = env.pfsServer.NewPipelineFinisher(txnCtx)
		}

		err := cb(txnCtx)
		if err != nil {
			return err
		}
		return txnCtx.finish()
	})
	return err
}

// WithReadContext will call the given callback with a TransactionContext
// which can be used to perform reads of the current cluster state. If the
// transaction is used to perform any writes, they will be silently discarded.
func (env *TransactionEnv) WithReadContext(ctx context.Context, cb func(*TransactionContext) error) error {
	return col.NewDryrunSTM(ctx, env.serviceEnv.GetEtcdClient(), func(stm col.STM) error {
		pachClient := env.serviceEnv.GetPachClient(ctx)
		txnCtx := &TransactionContext{
			Client:         pachClient,
			ClientContext:  pachClient.Ctx(),
			Stm:            stm,
			commitFinisher: nil, // don't alter any pipeline commits in a read-only setting
			txnEnv:         env,
		}
		if env.pfsServer != nil {
			txnCtx.pfsPropagater = env.pfsServer.NewPropagater(stm)
		}

		err := cb(txnCtx)
		if err != nil {
			return err
		}
		return txnCtx.finish()
	})
}
