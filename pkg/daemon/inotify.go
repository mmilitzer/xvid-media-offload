package daemon

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

func (d *Daemon) startInotify() error {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return err
	}
	d.inotifyFd = fd
	return nil
}

func (d *Daemon) stopInotify() {
	if d.inotifyFd >= 0 {
		unix.Close(d.inotifyFd)
		d.inotifyFd = -1
	}
}

func (d *Daemon) addInotifyWatch(dir string) error {
	d.watchesMu.Lock()
	defer d.watchesMu.Unlock()

	if d.inotifyFd < 0 {
		return nil // inotify not available
	}

	if _, ok := d.watches[dir]; ok {
		return nil // already watching
	}

	mask := uint32(unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_IGNORED)
	wd, err := unix.InotifyAddWatch(d.inotifyFd, dir, mask)
	if err != nil {
		return err
	}

	d.watches[dir] = wd
	return nil
}

func (d *Daemon) inotifyReader() {
	defer d.producerWg.Done()

	if d.inotifyFd < 0 {
		return
	}

	var buf [(unix.SizeofInotifyEvent + unix.NAME_MAX + 1) * 20]byte

	for {
		select {
		case <-d.ctx.Done():
			return
		default:
		}

		// Poll with 1-second timeout so we can check for shutdown regularly.
		pfd := []unix.PollFd{{Fd: int32(d.inotifyFd), Events: unix.POLLIN}}
		_, err := unix.Poll(pfd, 1000)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			log.Printf("inotify poll error: %v", err)
			return
		}

		if pfd[0].Revents&(unix.POLLNVAL|unix.POLLERR) != 0 {
			return
		}
		if pfd[0].Revents&unix.POLLIN == 0 {
			continue
		}

		n, err := unix.Read(d.inotifyFd, buf[:])
		if err != nil {
			if err == unix.EAGAIN || err == unix.EINTR {
				continue
			}
			log.Printf("inotify read error: %v", err)
			return
		}
		if n <= 0 {
			continue
		}

		offset := 0
		for offset < n {
			event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			nameLen := int(event.Len)
			if event.Mask&unix.IN_Q_OVERFLOW != 0 {
				log.Printf("inotify: queue overflow detected")
				select {
				case d.overflowRescan <- struct{}{}:
				default:
				}
			}
			if nameLen > 0 {
				nameBytes := buf[offset+unix.SizeofInotifyEvent : offset+unix.SizeofInotifyEvent+nameLen]
				name := string(bytes.TrimRight(nameBytes, "\x00"))
				d.handleInotifyEvent(int(event.Wd), name, event.Mask)
			}
			offset += unix.SizeofInotifyEvent + nameLen
		}
	}
}

func (d *Daemon) handleInotifyEvent(wd int, name string, mask uint32) {
	if mask&unix.IN_IGNORED != 0 {
		d.watchesMu.Lock()
		for dir, id := range d.watches {
			if id == wd {
				delete(d.watches, dir)
				break
			}
		}
		d.watchesMu.Unlock()
		return
	}

	if mask&unix.IN_DELETE == 0 && mask&unix.IN_MOVED_FROM == 0 {
		return
	}

	if !strings.HasSuffix(name, ".offloaded") {
		return
	}

	d.watchesMu.Lock()
	var dir string
	for dDir, id := range d.watches {
		if id == wd {
			dir = dDir
			break
		}
	}
	d.watchesMu.Unlock()

	if dir == "" {
		return
	}

	base := strings.TrimSuffix(name, ".offloaded")
	filePath := filepath.Join(dir, base)

	log.Printf("inotify: .offloaded deleted for %s", filePath)

	job, ok := d.resolveRestoreJob(filePath)
	if !ok {
		log.Printf("inotify: unable to resolve restore job for %s", filePath)
		return
	}

	d.enqueueRestore(job)
}
