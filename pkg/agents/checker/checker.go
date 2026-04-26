package checker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/adnl"
	"github.com/xssnick/tonutils-go/adnl/dht"
	"github.com/xssnick/tonutils-go/adnl/keys"
	"github.com/xssnick/tonutils-go/adnl/overlay"
	"github.com/xssnick/tonutils-go/adnl/rldp"
	"github.com/xssnick/tonutils-go/tl"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"
	"github.com/xssnick/tonutils-storage-provider/pkg/transport"
	"github.com/xssnick/tonutils-storage/storage"

	"mytonprovider-backend/pkg/constants"
	"mytonprovider-backend/pkg/models/db"
	"mytonprovider-backend/pkg/utils"
)

const (
	maxConcurrentProviderChecks = 30
	maxConcurrentBagChecks      = 30
	providerResponseTimeout     = 14 * time.Second
	dhtTimeout                  = 14 * time.Second
	pingTimeout                 = 7 * time.Second
	rlQueryTimeout              = 10 * time.Second
	verifyStorageRetries        = 3
)

type Checker struct {
	prv            ed25519.PrivateKey
	providerClient *transport.Client
	dhtClient      *dht.Client
	logger         *slog.Logger
}

type StorageProofResult struct {
	ProviderIPs          []db.ProviderIP
	ContractProofsChecks []db.ContractProofsCheck
}

func New(privateKey ed25519.PrivateKey, providerClient *transport.Client, dhtClient *dht.Client, logger *slog.Logger) *Checker {
	return &Checker{
		prv:            privateKey,
		providerClient: providerClient,
		dhtClient:      dhtClient,
		logger:         logger,
	}
}

func (c *Checker) CheckStorageProofs(ctx context.Context, storageContracts []db.ContractToProviderRelation) (result StorageProofResult, err error) {
	if len(storageContracts) == 0 {
		return
	}

	availableProvidersIPs, err := c.findProvidersIPs(ctx, storageContracts)
	if err != nil {
		return
	}

	for _, ip := range availableProvidersIPs {
		result.ProviderIPs = append(result.ProviderIPs, ip)
	}

	result.ContractProofsChecks = c.checkActiveContracts(ctx, storageContracts, availableProvidersIPs)
	return
}

func (c *Checker) findProvidersIPs(ctx context.Context, storageContracts []db.ContractToProviderRelation) (availableProvidersIPs map[string]db.ProviderIP, err error) {
	log := c.logger.With(slog.String("agent_function", "findProvidersIPs"))
	uniqueProviders := make(map[string]db.ContractToProviderRelation)
	for _, sc := range storageContracts {
		if _, exists := uniqueProviders[sc.ProviderPublicKey]; !exists {
			uniqueProviders[sc.ProviderPublicKey] = sc
		}
	}

	availableProvidersIPs = make(map[string]db.ProviderIP, len(uniqueProviders))
	notFoundIPs := make([]string, 0)
	semaphore := make(chan struct{}, maxConcurrentProviderChecks)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, sc := range uniqueProviders {
		wg.Add(1)
		go func(contract db.ContractToProviderRelation) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			providerIPs, pErr := c.findProviderIPs(ctx, contract, log)
			if pErr != nil {
				mu.Lock()
				notFoundIPs = append(notFoundIPs, contract.ProviderPublicKey)
				availableProvidersIPs[contract.ProviderPublicKey] = providerIPs
				mu.Unlock()
				return
			}

			mu.Lock()
			availableProvidersIPs[contract.ProviderPublicKey] = providerIPs
			mu.Unlock()
		}(sc)
	}

	wg.Wait()

	for _, pk := range notFoundIPs {
		ip := availableProvidersIPs[pk]
		if ip.Provider.IP == "" {
			log.Info("provider IP not found", "provider_pubkey", pk)
			delete(availableProvidersIPs, pk)
			continue
		}

		providerContracts := make([]db.ContractToProviderRelation, 0)
		for _, sc := range storageContracts {
			if sc.ProviderPublicKey == pk {
				providerContracts = append(providerContracts, sc)
			}
		}

		storageIP, sErr := c.findStorageIPOverlay(ctx, ip.Provider.IP, providerContracts, log)
		if sErr != nil {
			log.Error("failed to find storage IP via overlay", "provider_pubkey", pk, "error", sErr)
			delete(availableProvidersIPs, pk)
			continue
		}

		ip.Storage = storageIP
		availableProvidersIPs[pk] = ip
	}

	return
}

func (c *Checker) checkActiveContracts(ctx context.Context, storageContracts []db.ContractToProviderRelation, availableProvidersIPs map[string]db.ProviderIP) []db.ContractProofsCheck {
	log := c.logger.With(slog.String("agent_function", "checkActiveContracts"))
	providersContracts := make(map[string][]db.ContractToProviderRelation)
	for _, sc := range storageContracts {
		providersContracts[sc.ProviderPublicKey] = append(providersContracts[sc.ProviderPublicKey], sc)
	}

	wg := sync.WaitGroup{}
	semaphore := make(chan struct{}, maxConcurrentBagChecks)
	var bagsStatuses sync.Map

	for pubkey, contracts := range providersContracts {
		wg.Add(1)
		go func(pubkey string, contracts []db.ContractToProviderRelation) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			gw := adnl.NewGateway(c.prv)
			defer gw.Close()

			if sErr := gw.StartClient(); sErr != nil {
				log.Error("failed to start ADNL gateway", "error", sErr)
				return
			}

			ip, ok := availableProvidersIPs[pubkey]
			if !ok {
				fillStatuses(&bagsStatuses, contracts, constants.IPNotFound)
				return
			}

			checkProviderFiles(ctx, gw, ip, contracts, &bagsStatuses, log)
		}(pubkey, contracts)
	}

	wg.Wait()

	contractProofsChecks := make([]db.ContractProofsCheck, 0, len(storageContracts))
	bagsStatuses.Range(func(_, value any) bool {
		proof, ok := value.(db.ContractProofsCheck)
		if ok {
			contractProofsChecks = append(contractProofsChecks, proof)
		}
		return true
	})

	return contractProofsChecks
}

func (c *Checker) findStorageIPOverlay(ctx context.Context, providerIP string, contracts []db.ContractToProviderRelation, log *slog.Logger) (ip db.IPInfo, err error) {
	if len(contracts) == 0 {
		return ip, fmt.Errorf("no contracts provided")
	}

	bagsToCheck := len(contracts)
	switch {
	case len(contracts) > 100:
		bagsToCheck = max(1, len(contracts)*10/100)
	case len(contracts) > 5:
		bagsToCheck = max(1, len(contracts)*20/100)
	}

	shuffled := make([]db.ContractToProviderRelation, len(contracts))
	copy(shuffled, contracts)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	for i := 0; i < bagsToCheck && i < len(shuffled); i++ {
		sc := shuffled[i]
		bag, dErr := hex.DecodeString(sc.BagID)
		if dErr != nil {
			log.Error("failed to decode bag ID", "bag_id", sc.BagID, "error", dErr)
			continue
		}

		dhtTimeoutCtx, cancel := context.WithTimeout(ctx, dhtTimeout)
		nodesList, _, fErr := c.dhtClient.FindOverlayNodes(dhtTimeoutCtx, bag)
		cancel()
		if fErr != nil {
			if !errors.Is(fErr, dht.ErrDHTValueIsNotFound) {
				log.Error("failed to find bag overlay nodes", "bag_id", sc.BagID, "error", fErr)
			}
			continue
		}

		if nodesList == nil || len(nodesList.List) == 0 {
			continue
		}

		for _, node := range nodesList.List {
			key, ok := node.ID.(keys.PublicKeyED25519)
			if !ok {
				continue
			}

			adnlID, hErr := tl.Hash(key)
			if hErr != nil {
				log.Error("failed to hash overlay key", "error", hErr)
				continue
			}

			dhtTimeoutCtx2, cancel2 := context.WithTimeout(ctx, dhtTimeout)
			addrList, pubKey, fErr := c.dhtClient.FindAddresses(dhtTimeoutCtx2, adnlID)
			cancel2()
			if fErr != nil {
				if !errors.Is(fErr, dht.ErrDHTValueIsNotFound) {
					log.Debug("failed to find addresses in DHT", "error", fErr)
				}
				continue
			}

			if addrList == nil || len(addrList.Addresses) == 0 {
				continue
			}

			for _, addr := range addrList.Addresses {
				if addr.IP.String() == providerIP {
					ip.PublicKey = pubKey
					ip.IP = addr.IP.String()
					ip.Port = addr.Port
					return
				}
			}
		}
	}

	return ip, fmt.Errorf("storage IP not found via overlay DHT after checking %d bags", bagsToCheck)
}

func (c *Checker) findProviderIPs(ctx context.Context, sc db.ContractToProviderRelation, log *slog.Logger) (result db.ProviderIP, err error) {
	result.PublicKey = sc.ProviderPublicKey
	addr, err := address.ParseAddr(sc.Address)
	if err != nil {
		return result, fmt.Errorf("failed to parse address %s: %w", sc.Address, err)
	}

	pk, err := hex.DecodeString(sc.ProviderPublicKey)
	if err != nil {
		return result, fmt.Errorf("failed to decode provider public key: %w", err)
	}

	result.Provider, err = c.findProviderIP(ctx, pk)
	if err != nil {
		return result, fmt.Errorf("failed to verify provider IP: %w", err)
	}

	result.Storage, err = c.findStorageIP(ctx, addr, pk)
	if err != nil {
		return result, fmt.Errorf("failed to find storage IP: %w", err)
	}

	return
}

func (c *Checker) findStorageIP(ctx context.Context, addr *address.Address, pk []byte) (ip db.IPInfo, err error) {
	var proof []byte
	err = utils.TryNTimes(func() (cErr error) {
		timeoutCtx, cancel := context.WithTimeout(ctx, providerResponseTimeout)
		defer cancel()

		proof, cErr = c.providerClient.VerifyStorageADNLProof(timeoutCtx, pk, addr)
		return
	}, verifyStorageRetries)
	if err != nil {
		return ip, fmt.Errorf("failed to verify storage adnl proof: %w", err)
	}

	dhtTimeoutCtx, cancel := context.WithTimeout(ctx, dhtTimeout)
	defer cancel()
	l, pub, err := c.dhtClient.FindAddresses(dhtTimeoutCtx, proof)
	if err != nil {
		return ip, fmt.Errorf("failed to find addresses in dht: %w", err)
	}

	if l == nil || len(l.Addresses) == 0 {
		return ip, fmt.Errorf("no storage addresses found")
	}

	ip.PublicKey = pub
	ip.IP = l.Addresses[0].IP.String()
	ip.Port = l.Addresses[0].Port
	return
}

func (c *Checker) findProviderIP(ctx context.Context, pk []byte) (ip db.IPInfo, err error) {
	channelKeyID, err := tl.Hash(keys.PublicKeyED25519{Key: pk})
	if err != nil {
		return ip, fmt.Errorf("failed to calc hash of provider key: %w", err)
	}

	dhtTimeoutCtx, cancel := context.WithTimeout(ctx, dhtTimeout)
	defer cancel()
	dhtVal, _, err := c.dhtClient.FindValue(dhtTimeoutCtx, &dht.Key{
		ID:    channelKeyID,
		Name:  []byte("storage-provider"),
		Index: 0,
	})
	if err != nil {
		return ip, fmt.Errorf("failed to find storage-provider in dht: %w", err)
	}

	var nodeAddr transport.ProviderDHTRecord
	if _, pErr := tl.Parse(&nodeAddr, dhtVal.Data, true); pErr != nil {
		return ip, fmt.Errorf("failed to parse node dht value: %w", pErr)
	}

	if len(nodeAddr.ADNLAddr) == 0 {
		return ip, fmt.Errorf("no adnl addresses in node dht value")
	}

	dhtTimeoutCtx2, cancel2 := context.WithTimeout(ctx, dhtTimeout)
	defer cancel2()
	l, pub, fErr := c.dhtClient.FindAddresses(dhtTimeoutCtx2, nodeAddr.ADNLAddr)
	if fErr != nil {
		return ip, fmt.Errorf("failed to find adnl addresses in dht: %w", fErr)
	}

	if l == nil || len(l.Addresses) == 0 {
		return ip, fmt.Errorf("no provider addresses found")
	}

	ip.PublicKey = pub
	ip.IP = l.Addresses[0].IP.String()
	ip.Port = l.Addresses[0].Port
	return
}

func fillStatuses(bagsStatuses *sync.Map, contracts []db.ContractToProviderRelation, reason constants.ReasonCode) {
	for _, sc := range contracts {
		bagsStatuses.Store(sc.ProviderAddress+sc.BagID, db.ContractProofsCheck{
			ContractAddress: sc.Address,
			ProviderAddress: sc.ProviderAddress,
			Reason:          reason,
		})
	}
}

func getKey(bagID, ip string, port int32) string {
	return ip + ":" + strconv.Itoa(int(port)) + "/" + bagID
}

func checkProviderFiles(ctx context.Context, gw *adnl.Gateway, ip db.ProviderIP, storageContracts []db.ContractToProviderRelation, bagsStatuses *sync.Map, log *slog.Logger) {
	log = log.With(slog.String("provider_pubkey", ip.PublicKey))
	stats := make(map[constants.ReasonCode]int)
	maxFailureThreshold := uint32(float32(len(storageContracts)) / 100.0 * 20.0)
	var failsInARow uint32

	addr := ip.Storage.IP + ":" + strconv.Itoa(int(ip.Storage.Port))
	peer, rErr := gw.RegisterClient(addr, ip.Storage.PublicKey)
	if rErr != nil {
		log.Debug("failed to create ADNL peer", "error", rErr)
		fillStatuses(bagsStatuses, storageContracts, constants.CantCreatePeer)
		return
	}

	pingCtx, pingCancel := context.WithTimeout(ctx, pingTimeout)
	_, pErr := peer.Ping(pingCtx)
	pingCancel()
	if pErr != nil {
		log.Debug("initial provider ping failed", "error", pErr)
		fillStatuses(bagsStatuses, storageContracts, constants.FailedInitialPing)
		return
	}

	rl := rldp.NewClientV2(peer)
	defer rl.Close()

	for _, sc := range storageContracts {
		statusKey := getKey(sc.BagID, ip.Storage.IP, ip.Storage.Port)
		if failsInARow > maxFailureThreshold {
			bagsStatuses.Store(statusKey, db.ContractProofsCheck{
				ContractAddress: sc.Address,
				ProviderAddress: sc.ProviderAddress,
				Reason:          constants.UnavailableProvider,
			})
			continue
		}

		reason := checkPiece(ctx, rl, sc.BagID, log)
		bagsStatuses.Store(statusKey, db.ContractProofsCheck{
			ContractAddress: sc.Address,
			ProviderAddress: sc.ProviderAddress,
			Reason:          reason,
		})

		stats[reason]++
		if reason == constants.ValidStorageProof {
			failsInARow = 0
		} else {
			failsInARow++
		}

		time.Sleep(500 * time.Millisecond)
	}

	for reason, count := range stats {
		log.Debug("checked provider files", "reason", int(reason), "count", count)
	}
}

func checkPiece(ctx context.Context, rl *rldp.RLDP, bagID string, log *slog.Logger) (reason constants.ReasonCode) {
	reason = constants.NotFound
	peer, ok := rl.GetADNL().(adnl.Peer)
	if !ok {
		log.Error("failed to get ADNL peer")
		return constants.UnknownPeer
	}

	peer.Reinit()
	est := time.Now()
	pingCtx, pc := context.WithTimeout(ctx, pingTimeout)
	_, err := peer.Ping(pingCtx)
	pc()
	if err != nil {
		log.Debug("ping to provider failed", "error", err)
		return constants.PingFailed
	}

	bag, dErr := hex.DecodeString(bagID)
	if dErr != nil {
		log.Error("failed to decode bag ID", "error", dErr)
		return constants.InvalidBagID
	}

	over, err := tl.Hash(keys.PublicKeyOverlay{Key: bag})
	if err != nil {
		log.Debug("failed to hash overlay key", "error", err)
		return constants.InvalidBagID
	}

	if time.Since(est) > 5*time.Second {
		peer.Reinit()
		est = time.Now()
	}

	var res storage.TorrentInfoContainer
	rlCtx, rlc := context.WithTimeout(ctx, rlQueryTimeout)
	err = rl.DoQuery(rlCtx, 32<<20, overlay.WrapQuery(over, &storage.GetTorrentInfo{}), &res)
	rlc()
	if err != nil {
		log.Debug("failed to get torrent info from provider", "error", err)
		return constants.GetInfoFailed
	}

	cl, err := cell.FromBOC(res.Data)
	if err != nil {
		log.Debug("failed to parse BoC of torrent info", "error", err)
		return constants.InvalidHeader
	}

	if !bytes.Equal(cl.Hash(), bag) {
		log.Debug("hash not equal bag", "hash", cl.Hash(), "bag", bag)
		return constants.InvalidHeader
	}

	var info storage.TorrentInfo
	err = tlb.LoadFromCell(&info, cl.BeginParse())
	if err != nil {
		log.Debug("failed to load torrent info from cell", "error", err)
		return constants.InvalidHeader
	}

	pieceID := int32(1)
	var p int32
	if info.PieceSize != 0 {
		p = int32(info.FileSize / uint64(info.PieceSize))
	}
	if p != 0 {
		pieceID = rand.Int31n(p)
	}

	if time.Since(est) > 5*time.Second {
		peer.Reinit()
	}

	var piece storage.Piece
	rl2Ctx, rl2c := context.WithTimeout(ctx, rlQueryTimeout)
	err = rl.DoQuery(rl2Ctx, 32<<20, overlay.WrapQuery(over, &storage.GetPiece{PieceID: pieceID}), &piece)
	rl2c()
	if err != nil {
		log.Debug("failed to get piece from provider", "error", err)
		return constants.CantGetPiece
	}

	proof, err := cell.FromBOC(piece.Proof)
	if err != nil {
		log.Debug("failed to parse BoC of piece", "error", err)
		return constants.CantParseBoC
	}

	err = cell.CheckProof(proof, info.RootHash)
	if err != nil {
		log.Debug("proof check failed", "error", err)
		return constants.ProofCheckFailed
	}

	return constants.ValidStorageProof
}
