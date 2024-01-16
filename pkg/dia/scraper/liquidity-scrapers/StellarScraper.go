package liquidityscrapers

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/diadata-org/diadata/pkg/dia"
	"github.com/diadata-org/diadata/pkg/dia/helpers/horizonhelper"
	models "github.com/diadata-org/diadata/pkg/model"
	"github.com/diadata-org/diadata/pkg/utils"
	"github.com/sirupsen/logrus"
	"github.com/stellar/go/clients/horizonclient"
	hProtocol "github.com/stellar/go/protocols/horizon"
)

const stellarDefaultTradeCursor = ""

type StellarScraper struct {
	horizonClient *horizonclient.Client
	relDB         *models.RelDB
	datastore     *models.DB
	poolChannel   chan dia.Pool
	doneChannel   chan bool
	blockchain    string
	exchangeName  string
	cursor        *string
	cachedAssets  map[string]dia.Asset
}

func NewStellarScraper(exchange dia.Exchange, relDB *models.RelDB, datastore *models.DB) *StellarScraper {
	var (
		horizonClient *horizonclient.Client
		poolChannel   = make(chan dia.Pool)
		doneChannel   = make(chan bool)
		scraper       *StellarScraper
	)

	cursor := utils.Getenv(strings.ToUpper(exchange.Name)+"_CURSOR", "")
	if cursor == "" {
		cursor = stellarDefaultTradeCursor
	}

	horizonClient = horizonclient.DefaultPublicNetClient

	scraper = &StellarScraper{
		horizonClient: horizonClient,
		relDB:         relDB,
		datastore:     datastore,
		poolChannel:   poolChannel,
		doneChannel:   doneChannel,
		exchangeName:  exchange.Name,
		cursor:        &cursor,
	}

	go func() {
		scraper.fetchPools()
	}()

	return scraper
}

func (scraper *StellarScraper) fetchPools() {
	page, err := scraper.horizonClient.LiquidityPools(horizonclient.LiquidityPoolsRequest{
		Cursor: *scraper.cursor,
	})
	if err != nil {
		log.Error(err)
		return
	}

	recordsFound := len(page.Embedded.Records) > 0
	for recordsFound {
		for _, stellarPool := range page.Embedded.Records {
			log.Infof("pool: %s", stellarPool.ID)

			pool, err := scraper.getDIAPool(stellarPool)
			if err != nil {
				log.Error(err)
				continue
			}

			// Determine USD liquidity.
			if pool.SufficientNativeBalance(GLOBAL_NATIVE_LIQUIDITY_THRESHOLD) {
				scraper.datastore.GetPoolLiquiditiesUSD(&pool, priceCache)
			}

			scraper.poolChannel <- pool
		}

		nextPage, err := scraper.horizonClient.NextLiquidityPoolsPage(page)
		if err != nil {
			log.Error(err)
			return
		}
		page = nextPage

		recordsFound = len(page.Embedded.Records) > 0
		log.Infof("found %d pools", len(page.Embedded.Records))
	}
	scraper.doneChannel <- true
}

func (scraper *StellarScraper) getDIAPool(stellarPool hProtocol.LiquidityPool) (dia.Pool, error) {
	assetvolumes := make([]dia.AssetVolume, len(stellarPool.Reserves))
	for i, stellarAsset := range stellarPool.Reserves {
		s := strings.SplitN(stellarAsset.Asset, ":", 2)
		if len(s) != 2 {
			return dia.Pool{}, fmt.Errorf("invalid asset format: %s", stellarAsset.Asset)
		}

		asset, err := getDIAAsset(scraper, s[0], s[1])
		if err != nil {
			return dia.Pool{}, fmt.Errorf("error getting DIA asset for %s: %v", stellarAsset.Asset, err)
		}

		volume, err := strconv.ParseFloat(stellarAsset.Amount, 64)
		if err != nil {
			return dia.Pool{}, fmt.Errorf("error parsing volume: %v", err)
		}

		assetvolumes[i] = dia.AssetVolume{
			Asset:  asset,
			Volume: volume,
			Index:  uint8(i),
		}
	}
	pool := dia.Pool{
		Exchange:     dia.Exchange{Name: scraper.exchangeName},
		Blockchain:   dia.BlockChain{Name: scraper.blockchain},
		Address:      stellarPool.ID,
		Assetvolumes: assetvolumes,
		Time:         time.Now(),
	}
	return pool, nil
}

func getDIAAsset(scraper *StellarScraper, assetCode string, assetIssuer string) (asset dia.Asset, err error) {
	assetID := fmt.Sprintf("%s-%s", assetCode, assetIssuer)
	asset, err = scraper.relDB.GetAsset(assetID, scraper.blockchain)
	if err == nil {
		return
	}

	logContext := logrus.Fields{"context": "StellarTomlReader"}
	assetInfoReader := &horizonhelper.StellarAssetInfo{
		Logger: log.WithFields(logContext),
	}
	asset, err = assetInfoReader.GetStellarAssetInfo(scraper.horizonClient, assetCode, assetIssuer, scraper.blockchain)
	if err != nil {
		log.WithFields(logContext).Warn("cannot fetch asset with ID ", assetID)
	}
	return
}

func (scraper *StellarScraper) Pool() chan dia.Pool {
	return scraper.poolChannel
}

func (scraper *StellarScraper) Done() chan bool {
	return scraper.doneChannel
}