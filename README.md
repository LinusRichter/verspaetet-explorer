# verspaetet-explorer

Explore the [verspaetet](https://github.com/LinusRichter/verspaetet) dataset: Deutsche Bahn delay snapshots for all ~5,400 German IRIS stations, published monthly on [HuggingFace](https://huggingface.co/datasets/LinusRichter404/verspaetet).

Scrub through time and watch how the communicated delay of a train evolved, look at a station board as it was at any moment, follow a train along its route, or compare lines and days.

![Knowledge-scrubber: move through time and watch the delay evolution of a train](img/scrubber.gif)

![Station board Frankfurt (Main) Hbf](img/board.png)

## Quick start

You need [Docker](https://docs.docker.com/get-docker/) (Docker Desktop on Windows and macOS) and about 3 GB of free disk space per month of data.

```bash
git clone https://github.com/LinusRichter/verspaetet-explorer.git
cd verspaetet-explorer
docker compose up --build
```

On the first start the explorer downloads the latest published month from HuggingFace, checks it against the published checksums and splits it into one file per day. That takes about 5–10 minutes per month and is shown in the log. When it is done, the log says:

```
verspaetet-explorer is ready: open http://localhost:8080
```

Later starts reuse the prepared data and are ready within seconds. Stop with `Ctrl+C` (or `docker compose down`).

The container is limited to 2 CPU cores and 2 GB of memory, so your computer stays usable while a month is being prepared.

## Settings

Set them in front of the command, e.g. `MONTHS=all docker compose up --build`, or in a `.env` file next to `compose.yaml`.

| Variable | Default | Meaning |
|---|---|---|
| `MONTHS` | `latest` | which months to use: `latest`, `all`, or a list like `2026-09,2026-10` |
| `PORT` | `8080` | port on your computer |

New months are published on the 4th of the following month; restart the explorer to fetch them. Data lives in the Docker volume `verspaetet-explorer_data`; `docker compose down -v` deletes it.

## How it works

- **Sync** (`internal/dataset`): lists the published months, downloads `stop_events.parquet` and `scrape_runs.parquet`, verifies their SHA-256 against `manifest.json`, then splits each month into day files in one pass. The month file is sorted by trip, so without this step every query would have to read a whole month.
- **Queries** (`internal/store`): an embedded DuckDB reads only the day files a request needs.
- **API and UI** (`internal/api`, `web/`): Go HTTP server with the board, journey, line, day and search endpoints, and a React UI served by the same process.

## Development

The Go server needs cgo for DuckDB, so the simplest way to build and test is inside Docker:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.26-bookworm go test ./...
```

Frontend with hot reload against a running explorer (`docker compose up`):

```bash
cd web
npm ci
npm run dev
```

Vite serves the UI on `http://localhost:5173` and forwards `/api` to `http://localhost:8080`.

## The data in one paragraph

Each month has `stop_events.parquet` (one row per *change* of a train's forecast at one station: planned/actual time, platform, route, cancellation), `scrape_runs.parquet` (the fetch log: one row per successful board fetch, so "unchanged" can be told apart from "not observed"), `manifest.json` (checksums, coverage, known downtimes) and a README. `stations.parquet` at the repo root is the station registry. Details: [dataset card](https://huggingface.co/datasets/LinusRichter404/verspaetet).

## License

Code: [MIT](LICENSE).

Data: Deutsche Bahn (Timetables API & StaDa API), license [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). Dataset: verspaetet, [huggingface.co/datasets/LinusRichter404/verspaetet](https://huggingface.co/datasets/LinusRichter404/verspaetet). Keep this attribution when you show or reuse the data.
