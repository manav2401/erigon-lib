/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package downloader

import (
	"context"
	"fmt"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/ledgerwatch/erigon-lib/gointerfaces"
	proto_downloader "github.com/ledgerwatch/erigon-lib/gointerfaces/downloader"
	prototypes "github.com/ledgerwatch/erigon-lib/gointerfaces/types"
	"github.com/ledgerwatch/log/v3"
	"google.golang.org/protobuf/types/known/emptypb"
)

var (
	_ proto_downloader.DownloaderServer = &GrpcServer{}
)

func NewGrpcServer(d *Downloader) (*GrpcServer, error) {
	return &GrpcServer{d: d}, nil
}

type GrpcServer struct {
	proto_downloader.UnimplementedDownloaderServer
	d *Downloader
}

// Download - create new .torrent ONLY if initialSync, everything else Erigon can generate by itself
func (s *GrpcServer) Download(ctx context.Context, request *proto_downloader.DownloadRequest) (*emptypb.Empty, error) {
	logEvery := time.NewTicker(20 * time.Second)
	defer logEvery.Stop()
	defer s.d.applyWebseeds()

	torrentClient := s.d.Torrent()
	snapDir := s.d.SnapDir()
	for i, it := range request.Items {
		if it.Path == "" {
			return nil, fmt.Errorf("field 'path' is required")
		}

		select {
		case <-logEvery.C:
			log.Info("[snapshots] initializing", "files", fmt.Sprintf("%d/%d", i, len(request.Items)))
		default:
		}

		if it.TorrentHash == nil {
			// if we dont have the torrent hash then we seed a new snapshot
			log.Info("[snapshots] seeding a new snapshot")
			ok, err := seedNewSnapshot(ctx, it.Path, torrentClient, snapDir)
			if err != nil {
				return nil, err
			}
			if ok {
				log.Debug("[snapshots] already have both seg and torrent file")
			} else {
				log.Warn("[snapshots] didn't get the seg or the torrent file")
			}
			continue
		}

		err := s.d.AddInfoHashAsMagnetLink(ctx, Proto2InfoHash(it.TorrentHash), it.Path)
		if err != nil {
			return nil, err
		}
	}
	s.d.ReCalcStats(10 * time.Second) // immediately call ReCalc to set stat.Complete flag
	return &emptypb.Empty{}, nil
}

func (s *GrpcServer) Verify(ctx context.Context, request *proto_downloader.VerifyRequest) (*emptypb.Empty, error) {
	err := s.d.VerifyData(ctx)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *GrpcServer) Stats(ctx context.Context, request *proto_downloader.StatsRequest) (*proto_downloader.StatsReply, error) {
	stats := s.d.Stats()
	return &proto_downloader.StatsReply{
		MetadataReady: stats.MetadataReady,
		FilesTotal:    stats.FilesTotal,

		Completed: stats.Completed,
		Progress:  stats.Progress,

		PeersUnique:      stats.PeersUnique,
		ConnectionsTotal: stats.ConnectionsTotal,

		BytesCompleted: stats.BytesCompleted,
		BytesTotal:     stats.BytesTotal,
		UploadRate:     stats.UploadRate,
		DownloadRate:   stats.DownloadRate,
	}, nil
}

func Proto2InfoHash(in *prototypes.H160) metainfo.Hash {
	return gointerfaces.ConvertH160toAddress(in)
}

// decides what we do depending on wether we have the .seg file or the .torrent file
// have .torrent no .seg => get .seg file from .torrent
// have .seg no .torrent => get .torrent from .seg
func seedNewSnapshot(ctx context.Context, name string, torrentClient *torrent.Client, snapDir string) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	// if we dont have the torrent file we build it if we have the .seg file
	if err := buildTorrentIfNeed(ctx, name, snapDir); err != nil {
		return false, err
	}

	// we add the .seg file we have and create the .torrent file if we dont have it
	ok, err := AddSegment(name, snapDir, torrentClient)
	if err != nil {
		return false, fmt.Errorf("AddSegment: %w", err)
	}

	// torrent file does exist and seg
	if !ok {
		return false, nil
	}

	// we skip the item in for loop since we build the seg and torrent file here
	return true, nil
}
