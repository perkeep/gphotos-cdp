gphotos-cdp
========

This program uses the Chrome DevTools Protocol to drive a Chrome session that
downloads your photos stored in Google Photos.
By default, it starts at the most ancient item in the library, and progresses
towards the most recent.
It can be run incrementally, as it keeps track of the last item that was
downloaded.
It only works with the main library for now, i.e. it does not support the photos
moved to Archive, or albums.
For each downloaded photo, an external program can be run on it (with the -run
flag) right after it is downloaded to e.g. upload it somewhere else. See the
upload/perkeep program, which uploads to a Perkeep server, for an example.


