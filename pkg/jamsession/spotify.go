package jamsession

import (
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/jamfactoryapp/jamfactory-backend/api/types"
	"github.com/jamfactoryapp/jamfactory-backend/pkg/notifications"
	"github.com/jamfactoryapp/jamfactory-backend/pkg/queue"
	"github.com/jamfactoryapp/jamfactory-backend/pkg/song"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/zmb3/spotify"
	"sync"
	"time"
)

var (
	ErrCollectionTypeInvalid     = errors.New("invalid collection type")
	ErrCouldNotGetAlbum          = errors.New("could not get album")
	ErrCouldNotGetAlbumTracks    = errors.New("could not get album tracks")
	ErrCouldNotGetPlaylistTracks = errors.New("could not get playlist tracks")
	ErrDeviceNotActive           = errors.New("device not active")
	ErrSongMalformed             = errors.New("malformed song")
	ErrVotingTypeInvalid         = errors.New("voting type invalid")
)

type SpotifyJamSession struct {
	sync.Mutex
	jamLabel   string
	name       string
	active     bool
	votingType types.VotingType
	client     spotify.Client
	queue      *queue.SpotifyQueue
	room       *notifications.Room
	quit       chan bool
}

func NewSpotify(client spotify.Client, label string) (JamSession, error) {
	u, err := client.CurrentUser()
	if err != nil {
		return nil, err
	}

	playerState, err := client.PlayerState()
	if err != nil {
		return nil, err
	}

	s := &SpotifyJamSession{
		jamLabel:   label,
		name:       fmt.Sprintf("%s's JamSession", u.DisplayName),
		active:     playerState.Device.ID != "",
		votingType: types.SessionVoting,
		client:     client,
		queue:      queue.NewSpotify(),
		room:       notifications.NewRoom(),
		quit:       make(chan bool),
	}

	go s.room.OpenDoors()

	return s, nil
}

func (s *SpotifyJamSession) Conductor() {
	for {
		select {
		case <-s.quit:
			return
		case <-time.After(time.Second):
			if s.active {
				playerState, err := s.client.PlayerState()
				if err != nil {
					continue
				}

				so, err := s.queue.GetNext()
				switch err {
				case nil:
					if (!playerState.Playing && playerState.Progress == 0) || playerState.Progress > playerState.Item.Duration-1000 {
						if err := s.Play(playerState.Device, so); err != nil {
							log.Error(err)
							continue
						}

						message := types.GetQueueResponse{Queue: s.Queue().Songs()}
						s.NotifyClients(&notifications.Message{
							Event:   notifications.Queue,
							Message: message,
						})
					}
				case queue.ErrQueueEmpty:
				default:
					log.Error(err)
					continue
				}

				s.NotifyClients(&notifications.Message{
					Event: notifications.Playback,
					Message: types.SocketPlaybackState{
						Playback: playerState,
					},
				})
			}
		}
	}
}

func (s *SpotifyJamSession) Play(device spotify.PlayerDevice, song song.Song) error {
	if !device.Active {
		return ErrDeviceNotActive
	}

	track, ok := song.Song().(*spotify.FullTrack)
	if !ok {
		return ErrSongMalformed
	}

	playOptions := spotify.PlayOptions{
		URIs: []spotify.URI{track.URI},
	}

	err := s.client.PlayOpt(&playOptions)
	if err != nil {
		return err
	}

	if err := s.queue.Advance(); err != nil {
		return err
	}

	return nil
}

func (s *SpotifyJamSession) JamLabel() string {
	return s.jamLabel
}

func (s *SpotifyJamSession) Name() string {
	return s.name
}

func (s *SpotifyJamSession) Active() bool {
	return s.active
}

func (s *SpotifyJamSession) VotingType() types.VotingType {
	return s.votingType
}

func (s *SpotifyJamSession) SetName(name string) {
	s.Lock()
	defer s.Unlock()
	s.name = name
}

func (s *SpotifyJamSession) SetVotingType(votingType string) error {
	s.Lock()
	defer s.Unlock()

	switch votingType {
	case string(types.SessionVoting):
		s.votingType = types.SessionVoting
	case string(types.IPVoting):
		s.votingType = types.IPVoting
	default:
		return ErrVotingTypeInvalid
	}

	return nil
}

func (s *SpotifyJamSession) SetActive(active bool) {
	s.Lock()
	defer s.Unlock()
	s.active = active
}

func (s *SpotifyJamSession) PlayerState() (*spotify.PlayerState, error) {
	return s.client.PlayerState()
}

func (s *SpotifyJamSession) DeviceID() (spotify.ID, error) {
	playerState, err := s.client.PlayerState()
	if err != nil {
		return "", err
	}

	return playerState.Device.ID, nil
}

func (s *SpotifyJamSession) SetDevice(id string) error {
	playerState, err := s.client.PlayerState()
	if err != nil {
		return err
	}
	deviceID := spotify.ID(id)
	if deviceID != playerState.Device.ID {
		err := s.client.TransferPlayback(deviceID, s.active)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SpotifyJamSession) SetState(state bool) error {
	s.Lock()
	defer s.Unlock()

	playerState, err := s.client.PlayerState()
	if err != nil {
		return err
	}

	if state == playerState.Playing {
		return nil
	}

	if state {
		err = s.client.Play()
	} else {
		err = s.client.Pause()
	}
	if err != nil {
		return err
	}

	s.active = state

	return nil
}

func (s *SpotifyJamSession) Delete() error {
	s.quit <- true
	return nil
}

func (s *SpotifyJamSession) CurrentSong() *spotify.FullTrack {
	if len(s.queue.Songs()) == 0 {
		return nil
	}
	return s.queue.Songs()[0].Song.(*spotify.FullTrack)
}

func (s *SpotifyJamSession) NotifyClients(msg *notifications.Message) {
	if len(s.room.Clients) > 0 {
		s.room.Broadcast <- msg
	}
}

func (s *SpotifyJamSession) Queue() queue.Queue {
	return s.queue
}

func (s *SpotifyJamSession) AddCollection(collectionType string, collectionID string) error {
	switch collectionType {
	case "playlist":
		playlist, err := s.client.GetPlaylistTracks(spotify.ID(collectionID))

		if err != nil {
			return ErrCouldNotGetPlaylistTracks
		}

		for i := 0; i < len(playlist.Tracks); i++ {
			so, err := s.GetSong(string(playlist.Tracks[i].Track.ID))
			if err != nil {
				return err
			}
			if err := s.queue.Vote(so.ID(), string(types.UserTypeHost), so.Song()); err != nil {
				return err
			}
		}

	case "album":
		album, err := s.client.GetAlbumTracks(spotify.ID(collectionID))

		if err != nil {
			return ErrCouldNotGetAlbum
		}

		ids := make([]spotify.ID, len(album.Tracks))
		for i := 0; i < len(album.Tracks); i++ {
			ids[i] = album.Tracks[i].ID
		}

		tracks, err := s.client.GetTracks(ids...)
		if err != nil {
			return ErrCouldNotGetAlbumTracks
		}

		for i := 0; i < len(tracks); i++ {
			so, err := s.GetSong(string(tracks[i].ID))
			if err != nil {
				return err
			}
			if err := s.queue.Vote(so.ID(), string(types.UserTypeHost), so.Song()); err != nil {
				return err
			}
		}

	default:
		return ErrCollectionTypeInvalid
	}
	s.NotifyClients(&notifications.Message{
		Event: notifications.Queue,
		Message: types.PutQueuePlaylistsResponse{
			Queue: s.Queue().Songs(),
		},
	})
	return nil
}

func (s *SpotifyJamSession) GetSong(songID string) (song.Song, error) {
	so, err := s.client.GetTrack(spotify.ID(songID))
	if err != nil {
		return nil, err
	}
	spotifySong := song.NewSpotify(so)
	return spotifySong, nil
}

func (s *SpotifyJamSession) Vote(songID string, voteID string) error {
	track, err := s.getTrack(songID)
	if err != nil {
		return err
	}

	if err := s.queue.Vote(string(track.ID), voteID, track); err != nil {
		return err
	}

	s.NotifyClients(&notifications.Message{
		Event: notifications.Queue,
		Message: types.PutQueueVoteResponse{
			Queue: s.Queue().Songs(),
		},
	})

	return nil
}

func (s *SpotifyJamSession) Devices() ([]spotify.PlayerDevice, error) {
	return s.client.PlayerDevices()
}

func (s *SpotifyJamSession) Playlists() (*spotify.SimplePlaylistPage, error) {
	return s.client.CurrentUsersPlaylists()
}

func (s *SpotifyJamSession) Search(index string, searchType spotify.SearchType, options *spotify.Options) (interface{}, error) {
	return s.client.SearchOpt(index, searchType, options)
}

func (s *SpotifyJamSession) IntroduceClient(conn *websocket.Conn) {
	client := notifications.NewClient(s.room, conn)
	client.Room.Register <- client

	go client.Write()
	go client.Read()
}

func (s *SpotifyJamSession) DeleteSong(songID string) error {
	so, err := s.GetSong(songID)
	if err != nil {
		return err
	}
	if err := s.queue.Delete(so.ID()); err != nil {
		return err
	}

	s.NotifyClients(&notifications.Message{
		Event: notifications.Queue,
		Message: types.PutQueuePlaylistsResponse{
			Queue: s.Queue().Songs(),
		},
	})
	return nil
}

func (s *SpotifyJamSession) getTrack(trackID string) (*spotify.FullTrack, error) {
	return s.client.GetTrack(spotify.ID(trackID))
}
