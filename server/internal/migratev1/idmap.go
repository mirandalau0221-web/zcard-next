package migratev1

// v1id_maps 讀寫 + 內存緩存（《數據遷移工具開發計劃》§2.4：內部 ID 不保留，
// 新舊對照走 v1id_maps；冪等以 (table_name, old_id) 存在性為準）。

import (
	"context"
	"fmt"
	"sync"

	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/v1idmap"
)

// IDMapper ID 映射器。cache 僅緩存「已確認寫入」的映射（Get 命中 DB 後也回填）。
// users/products/orders 等常駐鍵量級十萬級，全量緩存可控；cards 不建議走 Get 熱查。
type IDMapper struct {
	client *ent.Client
	mu     sync.RWMutex
	cache  map[string]map[uint64]uint64
}

// NewIDMapper 構造。
func NewIDMapper(client *ent.Client) *IDMapper {
	return &IDMapper{
		client: client,
		cache:  map[string]map[uint64]uint64{},
	}
}

// Get 查映射（緩存 → v1id_maps → 回填緩存）。
func (m *IDMapper) Get(ctx context.Context, table string, oldID uint64) (uint64, bool) {
	m.mu.RLock()
	newID, ok := m.cache[table][oldID]
	m.mu.RUnlock()
	if ok {
		return newID, true
	}
	row, err := m.client.V1IDMap.Query().
		Where(
			v1idmap.TableName(table),
			v1idmap.OldID(oldID),
		).
		Only(ctx)
	if err != nil {
		return 0, false
	}
	m.Remember(table, oldID, row.NewID)
	return row.NewID, true
}

// Put 寫映射（冪等：已存在時返回既有 newID，不重複插入）。
// c 允許傳事務客户端（loader 在行遷移同事務內寫入，保證原子）。
func (m *IDMapper) Put(ctx context.Context, c *ent.Client, table string, oldID, newID uint64) (uint64, error) {
	if existing, ok := m.Get(ctx, table, oldID); ok {
		return existing, nil
	}
	_, err := c.V1IDMap.Create().
		SetTableName(table).
		SetOldID(oldID).
		SetNewID(newID).
		Save(ctx)
	if err != nil {
		// 併發/重複插入撞唯一索引時兜底讀回
		if existing, ok := m.Get(ctx, table, oldID); ok {
			return existing, nil
		}
		return 0, fmt.Errorf("寫入 v1id_maps(%s,%d) 失敗: %w", table, oldID, err)
	}
	m.Remember(table, oldID, newID)
	return newID, nil
}

// GetBatch 批量查映射（大表批處理用：一次 IN 查詢回填緩存）。
// 返回 oldID → newID（僅含命中的）。
func (m *IDMapper) GetBatch(ctx context.Context, table string, oldIDs []uint64) map[uint64]uint64 {
	out := make(map[uint64]uint64, len(oldIDs))
	if len(oldIDs) == 0 {
		return out
	}
	var missing []uint64
	m.mu.RLock()
	cached := m.cache[table]
	for _, id := range oldIDs {
		if v, ok := cached[id]; ok {
			out[id] = v
		} else {
			missing = append(missing, id)
		}
	}
	m.mu.RUnlock()
	if len(missing) == 0 {
		return out
	}
	rows, err := m.client.V1IDMap.Query().
		Where(
			v1idmap.TableName(table),
			v1idmap.OldIDIn(missing...),
		).
		All(ctx)
	if err != nil {
		return out // 查詢失敗按全部未命中處理，調用方逐行兜底
	}
	for _, r := range rows {
		out[r.OldID] = r.NewID
		m.Remember(table, r.OldID, r.NewID)
	}
	return out
}

// PutBatch 批量寫映射（大表批處理：CreateBulk 一次插入）。
// 假定 (table, oldID) 均不存在（調用方已按 GetBatch 過濾）；整批失敗回退逐行。
func (m *IDMapper) PutBatch(ctx context.Context, c *ent.Client, table string, pairs [][2]uint64) error {
	if len(pairs) == 0 {
		return nil
	}
	builders := make([]*ent.V1IDMapCreate, len(pairs))
	for i, pr := range pairs {
		builders[i] = c.V1IDMap.Create().
			SetTableName(table).
			SetOldID(pr[0]).
			SetNewID(pr[1])
	}
	if _, err := c.V1IDMap.CreateBulk(builders...).Save(ctx); err != nil {
		for _, pr := range pairs {
			if _, err := m.Put(ctx, c, table, pr[0], pr[1]); err != nil {
				return err
			}
		}
	}
	for _, pr := range pairs {
		m.Remember(table, pr[0], pr[1])
	}
	return nil
}

// Remember 僅寫緩存（loader 已在事務內寫庫後同步緩存用）。
func (m *IDMapper) Remember(table string, oldID, newID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cache[table] == nil {
		m.cache[table] = map[uint64]uint64{}
	}
	m.cache[table][oldID] = newID
}
