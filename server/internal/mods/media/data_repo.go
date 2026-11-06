package media

// 數據倉儲（）：media/media_categories CRUD + 引用計數。

import (
	"context"
	"errors"
	"fmt"

	"github.com/NovaWorks/zcard-next/server/internal/data"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/media"
	"github.com/NovaWorks/zcard-next/server/internal/data/ent/mediacategory"
	"github.com/NovaWorks/zcard-next/server/internal/mods/media/port"
)

// 哨兵錯誤。
var (
	ErrNotFound     = errors.New("media: 記錄不存在")
	ErrCategoryName = errors.New("media.CATEGORY_NAME_INVALID: 名稱 1-30 字符")
	ErrHasChildren  = errors.New("media: 分類存在子分類或素材，禁止刪除")
	ErrReferenced   = errors.New("media.REFERENCED: 素材被引用，刪除需 confirm")
)

// MediaRepo 倉儲。
type MediaRepo struct {
	data *data.Data
}

// NewMediaRepo 構造。
func NewMediaRepo(d *data.Data) *MediaRepo { return &MediaRepo{data: d} }

// ── 分類 ──────────────────────────────────────────────────

// CreateCategory 建分類（名稱 1-30 字符）。
func (r *MediaRepo) CreateCategory(ctx context.Context, name string, parentID uint64, sort int32) (*ent.MediaCategory, error) {
	if len([]rune(name)) < 1 || len([]rune(name)) > 30 {
		return nil, ErrCategoryName
	}
	create := data.Client(ctx, r.data).MediaCategory.Create().
		SetName(name).SetSort(sort)
	if parentID > 0 {
		create.SetParentID(parentID)
	}
	return create.Save(ctx)
}

// RenameCategory 改名。
func (r *MediaRepo) RenameCategory(ctx context.Context, id uint64, name string) (*ent.MediaCategory, error) {
	if len([]rune(name)) < 1 || len([]rune(name)) > 30 {
		return nil, ErrCategoryName
	}
	c, err := data.Client(ctx, r.data).MediaCategory.UpdateOneID(id).SetName(name).Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// MoveCategory 移動（parentID=0 移到根；環狀拒絕）。
func (r *MediaRepo) MoveCategory(ctx context.Context, id, parentID uint64) error {
	client := data.Client(ctx, r.data)
	if id == parentID {
		return fmt.Errorf("media.CATEGORY_CYCLE")
	}
	// 環檢測：向上追溯 parent 鏈
	cur := parentID
	for cur != 0 {
		if cur == id {
			return fmt.Errorf("media.CATEGORY_CYCLE")
		}
		p, err := client.MediaCategory.Get(ctx, cur)
		if err != nil {
			break
		}
		cur = p.ParentID
	}
	upd := client.MediaCategory.UpdateOneID(id)
	if parentID > 0 {
		upd.SetParentID(parentID)
	} else {
		upd.ClearParentID()
	}
	_, err := upd.Save(ctx)
	return err
}

// DeleteCategory 刪除（有子分類或有素材時拒絕）。
func (r *MediaRepo) DeleteCategory(ctx context.Context, id uint64) error {
	client := data.Client(ctx, r.data)
	children, err := client.MediaCategory.Query().Where(mediacategory.ParentID(id)).Count(ctx)
	if err != nil {
		return err
	}
	files, err := client.Media.Query().Where(media.CategoryID(id)).Count(ctx)
	if err != nil {
		return err
	}
	if children > 0 || files > 0 {
		return ErrHasChildren
	}
	err = client.MediaCategory.DeleteOneID(id).Exec(ctx)
	if err != nil && ent.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

// ListCategories 分類樹（扁平全量，前端組樹）。
func (r *MediaRepo) ListCategories(ctx context.Context) ([]*ent.MediaCategory, error) {
	return data.Client(ctx, r.data).MediaCategory.Query().
		Order(ent.Asc(mediacategory.FieldSort)).
		All(ctx)
}

// ── 素材 ──────────────────────────────────────────────────

// CreateMedia 入庫（淨化後）。
func (r *MediaRepo) CreateMedia(ctx context.Context, in port.UploadInput, relPath, mime string, size int64, width, height int32, sha string) (*ent.Media, error) {
	create := data.Client(ctx, r.data).Media.Create().
		SetPath(relPath).
		SetName(in.Name).
		SetMime(mime).
		SetSize(size).
		SetSha256(sha).
		SetStorage(media.StorageLocal)
	if in.CategoryID > 0 {
		create.SetCategoryID(in.CategoryID)
	}
	if in.UploaderID > 0 {
		create.SetUploaderID(in.UploaderID)
	}
	if width > 0 {
		create.SetWidth(width)
	}
	if height > 0 {
		create.SetHeight(height)
	}
	return create.Save(ctx)
}

// ListMedia 列表（分類過濾/關鍵詞/分頁）。
func (r *MediaRepo) ListMedia(ctx context.Context, categoryID uint64, keyword string, page, size int, kinds ...string) ([]*ent.Media, int, error) {
	q := data.Client(ctx, r.data).Media.Query().Order(ent.Desc(media.FieldID))
	if categoryID > 0 {
		q = q.Where(media.CategoryID(categoryID))
	}
	if keyword != "" {
		q = q.Where(media.NameContains(keyword))
	}
	kind := "image"
	if len(kinds) > 0 {
		kind = kinds[0]
	}
	if kind != "all" {
		if kind == "video" {
			q.Where(media.MimeHasPrefix("video/"))
		} else {
			q.Where(media.MimeHasPrefix("image/"))
		}
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// ListUncategorized 僅未分類素材（category_id 為空；素材選擇器「未分類」視圖）。
func (r *MediaRepo) ListUncategorized(ctx context.Context, keyword string, page, size int, kinds ...string) ([]*ent.Media, int, error) {
	q := data.Client(ctx, r.data).Media.Query().
		Where(media.CategoryIDIsNil()).
		Order(ent.Desc(media.FieldID))
	if keyword != "" {
		q = q.Where(media.NameContains(keyword))
	}
	kind := "image"
	if len(kinds) > 0 {
		kind = kinds[0]
	}
	if kind != "all" {
		if kind == "video" {
			q.Where(media.MimeHasPrefix("video/"))
		} else {
			q.Where(media.MimeHasPrefix("image/"))
		}
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// GetMedia 單個。
func (r *MediaRepo) GetMedia(ctx context.Context, id uint64) (*ent.Media, error) {
	m, err := data.Client(ctx, r.data).Media.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return m, nil
}

// RenameMedia 改名（顯示名）。
func (r *MediaRepo) RenameMedia(ctx context.Context, id uint64, name string) (*ent.Media, error) {
	if name == "" || len([]rune(name)) > 255 {
		return nil, fmt.Errorf("media.NAME_INVALID")
	}
	m, err := data.Client(ctx, r.data).Media.UpdateOneID(id).SetName(name).Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return m, nil
}

// MoveMedia 批量移動分類。
func (r *MediaRepo) MoveMedia(ctx context.Context, ids []uint64, categoryID uint64) (int, error) {
	client := data.Client(ctx, r.data)
	q := client.Media.Update().Where(media.IDIn(ids...))
	if categoryID > 0 {
		q = q.SetCategoryID(categoryID)
	} else {
		q = q.ClearCategoryID()
	}
	n, err := q.Save(ctx)
	return n, err
}

// ListReferenced 被引用素材清單（刪除前展示）。
func (r *MediaRepo) ListReferenced(ctx context.Context, ids []uint64) ([]*ent.Media, error) {
	return data.Client(ctx, r.data).Media.Query().
		Where(media.IDIn(ids...), media.RefCountGT(0)).
		All(ctx)
}

// DeleteMedia 刪除（ref_count>0 且未 confirm → 拒絕並返回清單語義由 service 層組裝）。
func (r *MediaRepo) DeleteMedia(ctx context.Context, ids []uint64, force bool) (deleted int, refs []*ent.Media, err error) {
	var paths []string
	err = data.Tx(ctx, r.data, func(ctx context.Context) error {
		client := data.Client(ctx, r.data)
		var e error
		refs, e = client.Media.Query().Where(media.IDIn(ids...), media.RefCountGT(0)).All(ctx)
		if e != nil {
			return e
		}
		if len(refs) > 0 && !force {
			return ErrReferenced
		}
		rows, e := client.Media.Query().Where(media.IDIn(ids...)).All(ctx)
		if e != nil {
			return e
		}
		for _, m := range rows {
			q := client.Media.Delete().Where(media.ID(m.ID))
			if !force {
				q.Where(media.RefCountEQ(0))
			}
			n, e := q.Exec(ctx)
			if e != nil {
				return e
			}
			if n != 1 {
				refs = []*ent.Media{m}
				return ErrReferenced
			}
			deleted += n
			paths = append(paths, m.Path)
		}
		return nil
	})
	if err != nil {
		return 0, refs, err
	}
	for _, path := range paths {
		_ = DeleteLocal(path)
	}
	return deleted, nil, nil
}

// ── 引用計數（port.Referencer 實現）──────────────────────

// AddRefs 引用 +1。
func (r *MediaRepo) AddRefs(ctx context.Context, ids []uint64) error {
	return r.adjustRefs(ctx, ids, +1)
}

// ReleaseRefs 引用 -1（下限 0）。
func (r *MediaRepo) ReleaseRefs(ctx context.Context, ids []uint64) error {
	return r.adjustRefs(ctx, ids, -1)
}

func (r *MediaRepo) adjustRefs(ctx context.Context, ids []uint64, delta int32) error {
	if len(ids) == 0 {
		return nil
	}
	q := data.Client(ctx, r.data).Media.Update().Where(media.IDIn(ids...))
	if delta < 0 {
		q.Where(media.RefCountGT(0))
	}
	_, err := q.AddRefCount(delta).Save(ctx)
	return err
}
