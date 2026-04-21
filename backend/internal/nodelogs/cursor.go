package nodelogs

import (
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CursorRepo persists incremental-fetch positions.
type CursorRepo struct {
	db *gorm.DB
}

func NewCursorRepo(db *gorm.DB) *CursorRepo { return &CursorRepo{db: db} }

// LoadForNode returns a map keyed by (source, path) with the latest cursor.
func (r *CursorRepo) LoadForNode(nodeID uint) (map[CursorKey]Cursor, error) {
	var rows []model.NodeLogCursor
	if err := r.db.Where("node_id = ?", nodeID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[CursorKey]Cursor, len(rows))
	for _, row := range rows {
		out[CursorKey{row.Source, row.Path}] = Cursor{
			NodeID:     row.NodeID,
			Source:     row.Source,
			Path:       row.Path,
			CursorText: row.CursorText,
			FileOffset: row.FileOffset,
			FileInode:  row.FileInode,
		}
	}
	return out, nil
}

// SaveForNode upserts all cursors for a node.
func (r *CursorRepo) SaveForNode(nodeID uint, cs []Cursor) error {
	if len(cs) == 0 {
		return nil
	}
	rows := make([]model.NodeLogCursor, len(cs))
	for i, c := range cs {
		rows[i] = model.NodeLogCursor{
			NodeID:     nodeID,
			Source:     c.Source,
			Path:       c.Path,
			CursorText: c.CursorText,
			FileOffset: c.FileOffset,
			FileInode:  c.FileInode,
		}
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node_id"}, {Name: "source"}, {Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{"cursor_text", "file_offset", "file_inode", "updated_at"}),
	}).Create(&rows).Error
}
