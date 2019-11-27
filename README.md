gphotos-cdp
========

What?
--------

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


Why?
--------

We want to incrementally download our own photos out of Google Photos.

Google Photos used to have an API to do this (the Picasa Web Albums API) but
[they removed it](http://googlephotos.blogspot.com/2016/02/moving-on-from-picasa.html),
replacing it with a new API that doesn't let you download your
original photos. They instead let you download your photos
[with mangled EXIF, stripping location](https://developers.google.com/photos/library/guides/access-media-items#image-base-urls)
(and maybe recompressing the image bytes?).

There also used to be a way to sync your Google Photos to Google
Drive, and then you could use the Google Drive API to download your
original photos, but Google Photos
[removed that too](https://www.blog.google/products/photos/simplifying-google-photos-and-google-drive/).

We can get our original photos out with [Google Takeout](https://takeout.google.com/),
but only manually, and slowly. We don't want to have to remember to do
it (or remember to renew the time-limited scheduled takeouts) and we'd
like our photos mirrored in seconds or minutes, not weeks.

In [our original Perkeep
issue](https://github.com/perkeep/perkeep/issues/1144#issuecomment-525007239),
[@bradfitz](https://github.com/bradfitz/) said that we might have to give up on APIs and resort
to scraping, noting that the
[Chrome DevTools Protocol](https://github.com/ChromeDevTools/devtools-protocol) makes this
pretty easy. Brad hacked up some Go code to drive Chrome (using
https://github.com/chromedp/chromedp) and do a basic download and then
[Mathieu Lonjaret](https://github.com/mpl) made this tool, fleshing out the idea.

What if Google Photos breaks this tool on purpose or accident?
--------

I guess we'll have to continually update it.

But that's no different than using people's APIs, because companies all seem to
be deprecating and changing their APIs regularly too.

