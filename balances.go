package database

import (
	"errors"
	"math/big"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/google/uuid"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

type Balances struct {
	GUID         uuid.UUID      `gorm:"primaryKey" json:"guid"`
	Address      common.Address `json:"address" gorm:"serializer:bytes"`
	TokenAddress common.Address `json:"token_address" gorm:"serializer:bytes"`
	AddressType  uint8          `json:"address_type"` //0:用户地址；1:热钱包地址(归集地址)；2:冷钱包地址
	Balance      *big.Int       `gorm:"serializer:u256;column:balance" db:"balance" json:"Balance" form:"balance"`
	LockBalance  *big.Int       `gorm:"serializer:u256;column:lock_balance" db:"lock_balance" json:"LockBalance" form:"lock_balance"`
	Timestamp    uint64
}

type BalancesView interface {
	QueryWalletBalanceByTokenAndAddress(requestId string, addressType uint8, address, tokenAddress common.Address) (*Balances, error)
}

type BalancesDB interface {
	BalancesView

	UpdateOrCreate(string, []TokenBalance) error
	StoreBalances(string, []Balances) error
	UpdateBalances(string, []Balances) error
}

type balancesDB struct {
	gorm *gorm.DB
}

func NewBalancesDB(db *gorm.DB) BalancesDB {
	return &balancesDB{gorm: db}
}

func (db *balancesDB) StoreBalances(requestId string, balanceList []Balances) error {
	result := db.gorm.Table("balances_"+requestId).CreateInBatches(&balanceList, len(balanceList))
	return result.Error
}

func (db *balancesDB) UpdateBalances(requestId string, balanceList []Balances) error {
	for i := 0; i < len(balanceList); i++ {
		var balance = Balances{}
		log.Info("Address:", balanceList[i].Address, "TokenAddress:", balanceList[i].TokenAddress)
		result := db.gorm.Table("balances_" + requestId).Where(&Balances{Address: balanceList[i].Address, TokenAddress: balanceList[i].TokenAddress}).Take(&balance)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				return nil
			}
			return result.Error
		}
		balance.Balance = new(big.Int).Sub(balance.Balance, balanceList[i].LockBalance) //更新余额
		log.Info("new(big.Int).Sub(balance.Balance, balanceList[i].LockBalance)", balance.Balance, "---", balanceList[i].LockBalance)

		balance.LockBalance = new(big.Int).Add(balance.LockBalance, balanceList[i].LockBalance)
		err := db.gorm.Table("balances_" + requestId).Save(&balance).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func (db *balancesDB) QueryWalletBalanceByTokenAndAddress(requestId string, addressType uint8, address, tokenAddress common.Address) (*Balances, error) {
	var balanceEntry Balances
	err := db.gorm.Table("balances_"+requestId).Where("address = ? and token_address = ?", strings.ToLower(address.String()), strings.ToLower(tokenAddress.String())).Take(&balanceEntry).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			balanceItem := &Balances{
				GUID:         uuid.New(),
				Address:      address,
				TokenAddress: tokenAddress,
				AddressType:  addressType,
				Balance:      big.NewInt(0),
				LockBalance:  big.NewInt(0),
				Timestamp:    uint64(time.Now().Unix()),
			}
			err = db.gorm.Table("balances_" + requestId).Create(balanceItem).Error
			if err != nil {
				log.Error("create balance fail", "err", err)
				return nil, err
			}
			return balanceItem, nil
		}
		return nil, err
	}
	return &balanceEntry, nil
}

// 扫链更新余额
func (db *balancesDB) UpdateOrCreate(requestId string, balanceList []TokenBalance) error {
	for _, value := range balanceList {
		log.Info("Query wallet balance by token and address", "toAddress", value.ToAddress, "TokenAddress", value.TokenAddress, "Balance", value.Balance, "TxType", value.TxType)
		if value.TxType == "deposit" {
			userAddress, err := db.QueryWalletBalanceByTokenAndAddress(requestId, 0, value.ToAddress, value.TokenAddress)
			if err != nil {
				log.Error("Query user address fail", "err", err)
				return err
			}
			userAddress.Balance = new(big.Int).Add(userAddress.Balance, value.Balance)
			log.Info("Query user address success", "AddressType", userAddress.AddressType, "Address", userAddress.Address, "Balance", userAddress.Balance)

			errU := db.gorm.Table("balances_" + requestId).Save(&userAddress).Error
			if errU != nil {
				log.Error("Update user balance fail", "err", err)
				return errU
			}
		} else if value.TxType == "withdraw" {
			hotWalletAddress, err := db.QueryWalletBalanceByTokenAndAddress(requestId, 1, value.FromAddress, value.TokenAddress)
			if err != nil {
				log.Error("Query user address fail", "err", err)
				return err
			}
			hotWalletAddress.Balance = new(big.Int).Sub(hotWalletAddress.LockBalance, value.Balance)
			errU := db.gorm.Table("balances_" + requestId).Save(&hotWalletAddress).Error
			if errU != nil {
				log.Error("Update hot wallet balance fail", "err", err)
				return errU
			}
		} else if value.TxType == "collection" {
			userWalletAddress, err := db.QueryWalletBalanceByTokenAndAddress(requestId, 0, value.FromAddress, value.TokenAddress)
			if err != nil {
				log.Error("Query user address fail", "err", err)
				return err
			}
			userWalletAddress.LockBalance = new(big.Int).Sub(userWalletAddress.LockBalance, value.Balance) //疑问?这里为什么是锁仓金额减去转账金额
			errU := db.gorm.Table("balances_" + requestId).Save(&userWalletAddress).Error
			if errU != nil {
				log.Error("Update user balance fail", "err", err)
				return errU
			}

			hotWalletAddress, err := db.QueryWalletBalanceByTokenAndAddress(requestId, 1, value.ToAddress, value.TokenAddress)
			if err != nil {
				log.Error("Query hot wallet balance fail", "err", err)
				return err
			}
			hotWalletAddress.Balance = new(big.Int).Add(hotWalletAddress.Balance, value.Balance)

			errHot := db.gorm.Table("balances_" + requestId).Save(&hotWalletAddress).Error
			if errHot != nil {
				log.Error("Update hot wallet balance fail", "err", err)
				return errHot
			}
		} else if value.TxType == "hot2cold" {
			hotWalletAddress, err := db.QueryWalletBalanceByTokenAndAddress(requestId, 1, value.FromAddress, value.TokenAddress)
			if err != nil {
				log.Error("Query hot wallet info fail", "err", err)
				return err
			}
			hotWalletAddress.Balance = new(big.Int).Sub(hotWalletAddress.LockBalance, value.Balance)
			errHot := db.gorm.Table("balances_" + requestId).Save(&hotWalletAddress).Error
			if errHot != nil {
				log.Error("Update user balance fail", "err", err)
				return errHot
			}

			coldWalletAddress, err := db.QueryWalletBalanceByTokenAndAddress(requestId, 2, value.ToAddress, value.TokenAddress)
			if err != nil {
				log.Error("Query hot wallet balance fail", "err", err)
				return err
			}
			coldWalletAddress.Balance = new(big.Int).Add(coldWalletAddress.Balance, value.Balance)
			errCold := db.gorm.Table("balances_" + requestId).Save(&coldWalletAddress).Error
			if errCold != nil {
				log.Error("Update hot wallet balance fail", "err", err)
				return errCold
			}
		} else {
			coldWalletAddress, err := db.QueryWalletBalanceByTokenAndAddress(requestId, 2, value.ToAddress, value.TokenAddress)
			if err != nil {
				log.Error("Query hot wallet balance fail", "err", err)
				return err
			}
			coldWalletAddress.Balance = new(big.Int).Sub(coldWalletAddress.LockBalance, value.Balance)
			errCold := db.gorm.Table("balances_" + requestId).Save(&coldWalletAddress).Error
			if errCold != nil {
				log.Error("Update hot wallet balance fail", "err", err)
				return errCold
			}

			hotWalletAddress, err := db.QueryWalletBalanceByTokenAndAddress(requestId, 1, value.FromAddress, value.TokenAddress)
			if err != nil {
				log.Error("Query hot wallet info fail", "err", err)
				return err
			}
			hotWalletAddress.Balance = new(big.Int).Add(hotWalletAddress.Balance, value.Balance)
			errHot := db.gorm.Table("balances_" + requestId).Save(&hotWalletAddress).Error
			if errHot != nil {
				log.Error("Update user balance fail", "err", err)
				return errHot
			}
		}
	}
	return nil
}
