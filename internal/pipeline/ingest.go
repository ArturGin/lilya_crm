package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"

	"github.com/lilya/practice/internal/storage"
	"github.com/lilya/practice/internal/store"
)

// VideoExts are the accepted upload formats (anything ffmpeg reads would do,
// but these are what cameras and phones produce).
var VideoExts = map[string]bool{".mp4": true, ".mov": true, ".m4v": true, ".webm": true, ".mkv": true, ".avi": true}

var (
	ErrUnsupportedVideo = errors.New("supported video formats: mp4, mov, m4v, webm, mkv, avi")
	ErrEmptyFile        = errors.New("file is empty")
)

// IngestVideo stores r as the post's video (replacing a previous one) and
// queues processing; analysis follows automatically once it is processed.
func (p *Pipeline) IngestVideo(ctx context.Context, postID int64, uploaderID *int64, filename string, r io.Reader) (store.Asset, error) {
	filename = filepath.Base(filename)
	ext := strings.ToLower(filepath.Ext(filename))
	if !VideoExts[ext] {
		return store.Asset{}, ErrUnsupportedVideo
	}
	key := path.Join("videos", storage.NewID(), "original"+ext)
	size, err := p.Storage.Put(ctx, key, r)
	if err != nil {
		_ = p.Storage.Delete(ctx, key)
		return store.Asset{}, err
	}
	if size == 0 {
		_ = p.Storage.Delete(ctx, key)
		return store.Asset{}, ErrEmptyFile
	}
	asset, oldID, err := p.Store.AttachVideo(ctx, postID, store.NewAsset{
		Kind: store.AssetVideo, StorageKey: key, OriginalFilename: truncateRunes(filename, 255),
		ContentType: "video/" + strings.TrimPrefix(ext, "."), SizeBytes: size,
		Status: store.AssetUploaded, UploadedBy: uploaderID,
	})
	if err != nil {
		_ = p.Storage.Delete(ctx, key)
		return store.Asset{}, err
	}
	if oldID != nil {
		p.deleteAssets(ctx, []int64{*oldID})
	}
	if err := p.Worker.Enqueue(ctx, JobProcessVideo, ProcessPayload{AssetID: asset.ID}); err != nil {
		return asset, fmt.Errorf("queue processing: %w", err)
	}
	p.Log.Info("video uploaded, processing queued", "post_id", postID, "asset_id", asset.ID,
		"file", filename, "key", key, "size_mb", mb(size), "replaced_asset_id", oldID)
	return asset, nil
}
