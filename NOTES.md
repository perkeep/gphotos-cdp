# Notes for feature/listing PR

## TODO

*Move to Document as done.*

- Nomenclature, start,end,newest
- `navLeft()`: stabilize and accelerate
  - flag `-list`
  - remove `WaitReady("body",..)`
  - pass in prevousLocation (&string ?)
  - throw error if loc==prevLoc
  - exponential backoff (capped), timeout (default with param?)
- `navN()` batch timing and reload, `-batch` batchSize flag, default 1000
- flags: `-all`, `-list`, `-batch`, `-vt`, `-verify`
- flag `-vt`: (verboseTiming) Marginal Latency/Batch, navLeft: log.warning >3 iterations or >1s,1m
- skip if already downloaded (-optimistic)
- Termination criteria: lastPhoto(ctx)
  - lastPhoto: explain Selector
  - setup After Auth
  - add to `navN()`
- Future listing with DOM selectors, and scrolling

## Starting another profile

```bash
'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' --user-data-dir=/var/folders/bw/7rvbq3q92g5bn4lv4hrh5qv40000gn/T/gphotos-cdp
```

## Watching

- Show Histograms of navLeft <https://github.com/jamiealquiza/tachymeter>

```bash
cd ~/Downloads
for d in gphotos-cdp*; do echo $d $(find $d -type f|wc -l); done
while true; do cat gphotos-cdp/.lastdone ; echo; sleep 1; done
```

## Optimizing total time (peru)

- document.querySelector('a[href^="./photo/"]').href; // first photo
- document.querySelector('[aria-label="View previous photo"]')
- document.querySelector('[aria-label="View next photo"]');
- Pass in prevLocation to NavLeft
- ? Remove chromedp.WaitReady("body", chromedp.ByQuery)

### An Argument for Batching

```bash
$ time ./gphotos-cdp  -dev -n 6001 -list
21:35:11 Session Dir: /var/folders/bw/7rvbq3q92g5bn4lv4hrh5qv40000gn/T/gphotos-cdp
21:35:11 Download Dir: /Users/daniel/Downloads/gphotos-cdp
21:35:14 Last Photo: ./photo/AF1QipMWCRwtfPHED45inXlRNjeg28oyrfhW5UA60Qjg (first on Landing Page)
21:35:19 Nav to the end sequence is started because location is https://photos.google.com/photo/AF1QipNS8lcfYzAxV1ji4yY5noyzUUlznuNE4h-qktrJ
21:35:19 NavToLast iteration: location is https://photos.google.com/photo/AF1QipNS8lcfYzAxV1ji4yY5noyzUUlznuNE4h-qktrJ
21:36:26 . Marginal Avg Latency (last 1000 @ 1000): 66.43ms Cumulative Avg Latency: 66.43ms
21:37:40 . Marginal Avg Latency (last 1000 @ 2000): 73.99ms Cumulative Avg Latency: 70.21ms
21:39:09 . Marginal Avg Latency (last 1000 @ 3000): 88.97ms Cumulative Avg Latency: 76.46ms
21:42:48 . Marginal Avg Latency (last 1000 @ 4000): 218.75ms Cumulative Avg Latency: 112.03ms
21:48:38 . Marginal Avg Latency (last 1000 @ 5000): 350.23ms Cumulative Avg Latency: 159.67ms
22:05:27 . Marginal Avg Latency (last 1000 @ 6000): 1008.58ms Cumulative Avg Latency: 301.16ms
22:05:27 Rate (6001): 3.32/s Avg Latency: 301.11ms
OK
1815.896s
```

With page reloading every 1000 images:

```bash
$ time ./gphotos-cdp  -dev -n 6001 -list
21:24:45 Session Dir: /var/folders/bw/7rvbq3q92g5bn4lv4hrh5qv40000gn/T/gphotos-cdp
21:24:45 Download Dir: /Users/daniel/Downloads/gphotos-cdp
21:24:48 Last Photo: ./photo/AF1QipMWCRwtfPHED45inXlRNjeg28oyrfhW5UA60Qjg (first on Landing Page)
21:24:52 Nav to the end sequence is started because location is https://photos.google.com/photo/AF1QipNS8lcfYzAxV1ji4yY5noyzUUlznuNE4h-qktrJ
21:24:52 NavToLast iteration: location is https://photos.google.com/photo/AF1QipNS8lcfYzAxV1ji4yY5noyzUUlznuNE4h-qktrJ
21:25:55 . Marginal Avg Latency (last 1000 @ 1000): 62.11ms Cumulative Avg Latency: 62.11ms
21:27:02 . Marginal Avg Latency (last 1000 @ 2000): 65.13ms Cumulative Avg Latency: 63.62ms
21:28:06 . Marginal Avg Latency (last 1000 @ 3000): 62.51ms Cumulative Avg Latency: 63.25ms
21:29:10 . Marginal Avg Latency (last 1000 @ 4000): 63.23ms Cumulative Avg Latency: 63.25ms
21:30:16 . Marginal Avg Latency (last 1000 @ 5000): 63.60ms Cumulative Avg Latency: 63.32ms
21:31:19 . Marginal Avg Latency (last 1000 @ 6000): 62.00ms Cumulative Avg Latency: 63.10ms
21:31:21 Rate (6001): 15.85/s Avg Latency: 63.09ms
OK
396.036s
```

## Just Listing

```bash
# in batches

time for i in `seq 1 31`; do echo =-=- iteration $i start; time ./gphotos-cdp -v -dev -n 1000; echo =-=- iteration $i done; done

# From Scratch
2019/12/01 00:23:09 .. navLeft so far: 5m0.158025009s
2019/12/01 00:23:09 navLeft took 5m0.158049209s
2019/12/01 00:23:09 NavN iteration (7312): location is https://photos.google.com/photo/AF1QipORJSC-4iLwGPXtXiVMZsf2ZG8u6-XOegoijUDW
OK
4098.325s

# Appended:
2019/12/01 01:49:40 .. navLeft so far: 5m0.354725675s
2019/12/01 01:49:40 navLeft took 5m0.354868401s
2019/12/01 01:49:40 NavN iteration (7493): location is https://photos.google.com/photo/AF1QipN98MoLk2V3gkGxzJuSe8nWqy6U7nudpBxLkuFs
OK
4562.572s
# Append
2019/12/01 03:12:15 navLeft took 438.758367ms
2019/12/01 03:12:15 NavN iteration (5193): location is https://photos.google.com/photo/AF1QipP0NnL5F31yWiEMgQdx75VaDXMI-099eN-ePTEx
2019/12/01 03:12:15 Marking https://photos.google.com/photo/AF1QipP0NnL5F31yWiEMgQdx75VaDXMI-099eN-ePTEx as done
^C
1087.231s

# Append
2019/12/01 03:16:27 NavN iteration (2190): location is https://photos.google.com/photo/AF1QipPuPZJ-R6UTVgipHD8R-URdoB66hakxX0C4s1U7
2019/12/01 03:16:27 Marking https://photos.google.com/photo/AF1QipPuPZJ-R6UTVgipHD8R-URdoB66hakxX0C4s1U7 as done
2019/12/01 03:16:27 .. navLeft so far: 7.674008ms
2019/12/01 03:16:27 .. navLeft so far: 109.60942ms
2019/12/01 03:16:27 .. navLeft so far: 262.405953ms
2019/12/01 03:16:27 .. navLeft so far: 369.708306ms
2019/12/01 03:16:27 .. navLeft so far: 477.549416ms
2019/12/01 03:16:27 .. navLeft so far: 579.120186ms
2019/12/01 03:16:28 .. navLeft so far: 686.254498ms
^C
264.910s
# Append
2019/12/01 03:20:28 NavN iteration (2574): location is https://photos.google.com/photo/AF1QipMl1tMQVHpxfkgpPMEd_Ko8eiTE8IsyvFX0f0Rf
2019/12/01 03:20:28 Marking https://photos.google.com/photo/AF1QipMl1tMQVHpxfkgpPMEd_Ko8eiTE8IsyvFX0f0Rf as done
2019/12/01 03:20:28 navLeft took 196.956415ms
^C
177.288s

7312 + 7493 + 5193 + 2190 + 2574 + 4000 + 614
+1000 67.465s, 76.261s, 71.233s, 74.092s
+614

```

https://photos.google.com/lr/photo/AICs3TPpjUDPZuOJ7u_AWLKaVnXW-zrVAOWbUEhdZpQWvKKczTTYYEsR4zfqSwUDULOH1s3W18TneUiB_QFDidcpKr63abnUgA

https://photos.google.com/u/2/photo/AF1QipMWCRwtfPHED45inXlRNjeg28oyrfhW5UA60Qjg
AF1QipMWCRwtfPHED45inXlRNjeg28oyrfhW5UA60Qjg

