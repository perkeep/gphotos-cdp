# Notes for PR(daneroo) feature/listing branch

I don't expect this to be merged as-is. I mostly wanted to share my experiments and get feedback.

If any of it is useful, I can craft proper commits with smaller features.

*I am a first-time contributor, and not very experienced with Go, so would appreciate any feedback both on content and process.*

## Overview

When I first started to use the project, I experienced instability and would need to restart the download process a large number of times to get to the end. The process would timeout after 1000-4000 images. To test, I use two accounts one with ~700 images, and one with 31k images.

The timeouts were mostly coming from the `navLeft()` iterator, so I disabled actual downloading and focused on traversal. (`-list` option). I also added an option (`-all`) to bypass the incremental behavior of `$dldir/.lastDone`. I then added `-vt (VerboseTiming`, to isolate timing metrics reporting specifically.

The last main issue was the degradation of performance over time. Probably due to resource leaks in the webapp, especially when iterating at high speed. I found that the simplest solution to this was simply to force a reload every 1000 iterations. This typically takes about `1s` and avoids the `10X` slowdown I was observing.

The following are details of each of the major changes.

## `navLeft()`

- By adding the `-list` option, its functionality is isolated.
- Remove `WaitReady("body",..)`, as it has no effect (The document stays loaded when we navigate from image to image), and replace it with a loop (until `location != prevLocation`).
- When only listing, this is the critical part of the loop, so I spent some time experimenting with the timing of the chromedp interactions to optimize total throughput. What seemed to work best was an exponential backoff, so that we can benefit from the *most likely* fast navigation, but don't overtax the browser when we experience the occasional longer navigation delay (which can go up to multiple seconds).

- We also pass in `prevLoaction` from `navN()`, to support the new `navLeft()` loop, to optimize the throughput.

## `navN()`

- Addition of timing code
- Support listing only (`-list`)
- Reload page every (`batchSize=1000`) items.

## Termination Criteria

We explicitly fetch the lastPhoto, on the main album page (immediately after authentication), and rely on that, instead of `navLeft()` failing to navigate (`location == prevLocation`). The lastPhoto is determined with a CSS selector evaluation (`a[href^="./photo/"]`), which is fast and should be stable over time, as it uses the `<a href=.. >` detail navigation mechanism of the main album page. This opens another possibility for iteration on the album page itself. (see `listFromAlbum` below)

Another issue, although the `.lasDone` being captured on a successful run is useful, as photos are listed in EXIF date order. If older photos are added to the album they would not appear in subsequent runs. So it would be useful in general to rerun over the entire album (`-all`).

## Headless

If we don't need the authentication flow, (and we persist the auth creds in the profile `-dev`), Chrome can be brought up in `-headless` mode, which considerably reduces memory footprint (`>2Gb` to `<500Mb`), and marginally (23%) increases throughput for the listing experiment.

## `listFromAlbum()`

This is another experiment where the listing was entirely performed in the main album page (by scrolling incrementally). This is even faster. It would also allow iterating either forward or backward through the album.

In a further experiment, I would like to use this process as a coordinating mechanism and perform the actual downloads in separate (potentially multiple) `tabs/contexts`.

### Performance: An Argument for a periodic page reload and headless mode

Notice how the latency grows quickly without page reload (ms per iteration): [66, 74, 89, 219, 350, 1008,...], and the cumulative rate drops below `4 items/s` after `6000` items.

```bash
$ time ./gphotos-cdp  -dev -n 6001 -list
21:36:26 . Avg Latency (last 1000 @ 1000): Marginal: 66.43ms Cumulative: 66.43ms
21:37:40 . Avg Latency (last 1000 @ 2000): Marginal: 73.99ms Cumulative: 70.21ms
21:39:09 . Avg Latency (last 1000 @ 3000): Marginal: 88.97ms Cumulative: 76.46ms
21:42:48 . Avg Latency (last 1000 @ 4000): Marginal: 218.75ms Cumulative: 112.03ms
21:48:38 . Avg Latency (last 1000 @ 5000): Marginal: 350.23ms Cumulative: 159.67ms
22:05:27 . Avg Latency (last 1000 @ 6000): Marginal: 1008.58ms Cumulative: 301.16ms
22:05:27 Rate (6001): 3.32/s Avg Latency: 301.11ms
```

Whereas with page reloading every 1000 images, it is stable at `<60ms` with a sustained rate of `17 items/s`:

```bash
$ time ./gphotos-cdp  -dev  -vt -list -n 6001
14:33:22 . Avg Latency (last 1000 @ 1000):  Marginal: 58.88ms Cumulative: 58.88ms
14:34:21 . Avg Latency (last 1000 @ 2000):  Marginal: 57.60ms Cumulative: 58.24ms
14:35:21 . Avg Latency (last 1000 @ 3000):  Marginal: 57.93ms Cumulative: 58.14ms
14:36:17 . Avg Latency (last 1000 @ 4000):  Marginal: 54.93ms Cumulative: 57.33ms
14:37:16 . Avg Latency (last 1000 @ 5000):  Marginal: 56.24ms Cumulative: 57.12ms
14:38:16 . Avg Latency (last 1000 @ 6000):  Marginal: 58.73ms Cumulative: 57.38ms
14:38:16 Rate (6001): 17.43/s Avg Latency: 57.38ms
```

When invoked in `-headless` mode we can reduce latency to `~47ms` or `21 items/s`, also this reduces memory footprint from `>2Gb` to about `500Mb`

```bash
15:37:44 Rate (6001): 21.27/s Avg Latency: 47.02ms
```

When using `listFromAlbum()`, we can roughly double the iterations speed again to `38 items/s` or `25ms/item`

```bash
15:24:55 Rate (6001): 38.60/s Avg Latency: 25.91ms
```

*All of these times were obtained on a MacBook Air (2015) / 8G RAM.*

## Starting Chrome with another profile

```bash
'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' --user-data-dir=/var/folders/bw/7rvbq3q92g5bn4lv4hrh5qv40000gn/T/gphotos-cdp
```

## TODO

*Move to Document as done.*
- Review Nomenclature, start,end,newest,last,left,right...
- Refactor common chromedp uses in navLeft,navRight,navToEnd,navToLast
- `navLeft()` - throw error if loc==prevLoc
- flags: `-verify`, `-incr/-optimimistic`
- Listing with DOM selectors, and scrolling, while downloading in separate table
