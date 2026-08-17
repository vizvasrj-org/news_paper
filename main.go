package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // register PNG decoding with image.Decode
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"src/pkg/jsondata"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/signintech/gopdf"
	_ "golang.org/x/image/webp" // register WebP decoding with image.Decode
)

const userAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"

// jpegQuality controls the size/quality trade-off for the PDF's embedded
// page images (1-100). Newspaper scans are photo/text-heavy, so lossless
// PNG output made these PDFs enormous; JPEG at this quality is visually
// close to indistinguishable for reading purposes but a fraction of the
// size. Lower it (e.g. 60-70) for smaller files if 82 still isn't small
// enough for your needs.
const jpegQuality = 82

type Config struct {
	BaseURL      string
	LocationsURL string
	MaxDownloads int // Adjustable max concurrency
}

var config Config

var httpClient *http.Client

func init() {
	config = Config{
		BaseURL:      "https://epaper.livehindustan.com",
		LocationsURL: "https://epaperinhouse.livehindustan.com/be/api/v1/locations",
		MaxDownloads: 5,
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	httpClient = &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
	}
}

// =====================================================================
// New Next.js data-route response structures
//
// https://epaper.livehindustan.com/_next/data/{buildId}/edition/{slug}.json
//   ?date=YYYY-MM-DD&page=1&city={slug}
//
// Unlike the old /Home/GetAllpages endpoint, ViewerSrc below is already a
// plain, directly-downloadable URL — the AES-encrypted HrImageUrlJpg field
// and the decryption step that used to unwrap it are both gone.
// =====================================================================

type NextPageData struct {
	PageProps PageProps `json:"pageProps"`
}

type PageProps struct {
	CityOptions   []CityOption `json:"cityOptions"`
	Edition       EditionData  `json:"edition"`
	InitialPage   int          `json:"initialPage"`
	InitialSearch string       `json:"initialSearch"`
}

type CityOption struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Region string `json:"region"`
}

type EditionData struct {
	City         City         `json:"city"`
	IssueDateIso string       `json:"issueDateIso"`
	IssueLabel   string       `json:"issueLabel"`
	Pages        []EpaperPage `json:"pages"`
	TotalPages   int          `json:"totalPages"`
	EditionID    string       `json:"editionId"` // e.g. "RAN_HAZB"
}

type City struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Region string `json:"region"`
}

// EpaperPage is a single page of an edition.
type EpaperPage struct {
	ID           string `json:"id"`
	PageNumber   int    `json:"pageNumber"`
	Title        string `json:"title"`
	ViewerSrc    string `json:"viewerSrc"` // full-resolution .webp, ready to download
	ViewerWidth  int    `json:"viewerWidth"`
	ViewerHeight int    `json:"viewerHeight"`
	ThumbnailSrc string `json:"thumbnailSrc"`
	Alt          string `json:"alt"`
	IsJacket     bool   `json:"isJacket"`
}

// =====================================================================
// /be/api/v1/locations structures — used only to map whatever edition
// identifier the caller already has (old numeric EditionId, EditionCode
// like "RAN_HAZB", ...) onto the slug the new API needs.
// =====================================================================

type LocationGroup struct {
	ID              string            `json:"id"`
	OrgLocation     string            `json:"orgLocation"`
	LocationID      int               `json:"locationId"`
	EditionLocation []EditionLocation `json:"editionlocation"`
}

type EditionLocation struct {
	EditionLocation string        `json:"EditionLocation"`
	Editions        []EditionInfo `json:"edition"`
}

type EditionInfo struct {
	EditionName        string `json:"EditionName"`
	EditionDisplayName string `json:"EditionDisplayName"`
	EditionID          int    `json:"EditionId"`
	EditionCode        string `json:"EditionCode"`
}

// Slug returns the URL slug the new /edition/{slug} route expects.
// Empirically this is just the lowercased EditionName, e.g.
// "Hazaribagh" -> "hazaribagh", "Lucknow-Nagar" -> "lucknow-nagar".
// It isn't returned by the locations API itself, so we derive it here.
func (e EditionInfo) Slug() string {
	return strings.ToLower(strings.TrimSpace(e.EditionName))
}

func fetchLocations() ([]LocationGroup, error) {
	req, err := http.NewRequest("GET", config.LocationsURL, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create locations request")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", config.BaseURL+"/")
	req.Header.Set("Origin", config.BaseURL)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "locations request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("locations request failed with status %d", resp.StatusCode)
	}

	var groups []LocationGroup
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return nil, errors.Wrap(err, "failed to decode locations response")
	}
	return groups, nil
}

// editionIndex is a small in-memory cache over fetchLocations so we don't
// hit that endpoint on every request.
type editionIndex struct {
	mu      sync.RWMutex
	bySlug  map[string]EditionInfo
	byCode  map[string]EditionInfo
	byID    map[string]EditionInfo
	fetched time.Time
}

var editions = &editionIndex{}

const editionIndexTTL = 6 * time.Hour

func (idx *editionIndex) ensureFresh() error {
	idx.mu.RLock()
	stale := idx.bySlug == nil || time.Since(idx.fetched) > editionIndexTTL
	idx.mu.RUnlock()
	if !stale {
		return nil
	}

	groups, err := fetchLocations()
	if err != nil {
		return err
	}

	bySlug := make(map[string]EditionInfo)
	byCode := make(map[string]EditionInfo)
	byID := make(map[string]EditionInfo)
	for _, g := range groups {
		for _, el := range g.EditionLocation {
			for _, e := range el.Editions {
				bySlug[e.Slug()] = e
				byCode[e.EditionCode] = e
				byID[strconv.Itoa(e.EditionID)] = e
			}
		}
	}

	idx.mu.Lock()
	idx.bySlug, idx.byCode, idx.byID = bySlug, byCode, byID
	idx.fetched = time.Now()
	idx.mu.Unlock()
	return nil
}

// Resolve accepts whatever identifier a caller already has for an edition
// — an old numeric EditionId, an EditionCode such as "RAN_HAZB", or an
// already-correct slug — and returns the matching EditionInfo.
func (idx *editionIndex) Resolve(input string) (EditionInfo, error) {
	if err := idx.ensureFresh(); err != nil {
		return EditionInfo{}, err
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if e, ok := idx.bySlug[strings.ToLower(input)]; ok {
		return e, nil
	}
	if e, ok := idx.byCode[input]; ok {
		return e, nil
	}
	if e, ok := idx.byID[input]; ok {
		return e, nil
	}
	return EditionInfo{}, errors.Errorf("unknown edition %q", input)
}

// =====================================================================
// Next.js buildId — part of the data-route URL and rotated on every
// deploy, so it can't be hardcoded and has to be scraped off a live page.
// =====================================================================

var buildIDRe = regexp.MustCompile(`"buildId"\s*:\s*"([^"]+)"`)

type buildIDCache struct {
	mu      sync.RWMutex
	id      string
	fetched time.Time
}

var currentBuildID = &buildIDCache{}

const buildIDTTL = 30 * time.Minute

func (b *buildIDCache) Get(forceRefresh bool) (string, error) {
	b.mu.RLock()
	stale := forceRefresh || b.id == "" || time.Since(b.fetched) > buildIDTTL
	current := b.id
	b.mu.RUnlock()
	if !stale {
		return current, nil
	}

	id, err := fetchBuildID()
	if err != nil {
		if current != "" {
			// Refresh failed but we still have a last-known-good id — use it
			// rather than fail the whole request.
			return current, nil
		}
		return "", err
	}

	b.mu.Lock()
	b.id, b.fetched = id, time.Now()
	b.mu.Unlock()
	return id, nil
}

// fetchBuildID scrapes any edition page's __NEXT_DATA__ blob for the
// current Next.js buildId.
func fetchBuildID() (string, error) {
	req, err := http.NewRequest("GET", config.BaseURL+"/edition/delhi", nil)
	if err != nil {
		return "", errors.Wrap(err, "failed to create buildId request")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "buildId request failed")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "failed to read buildId response")
	}

	m := buildIDRe.FindSubmatch(body)
	if m == nil {
		return "", errors.New("buildId not found in edition page")
	}
	return string(m[1]), nil
}

// =====================================================================
// Edition data fetching
// =====================================================================

// GetData fetches the raw Next.js data payload for one edition/date.
// date must be "YYYY-MM-DD" — the old DD/MM/YYYY format doesn't apply
// to this endpoint.
func GetData(date, slug string) ([]byte, error) {
	id, err := currentBuildID.Get(false)
	if err != nil {
		return nil, errors.Wrap(err, "failed to resolve buildId")
	}

	body, status, err := fetchEditionJSON(id, date, slug)
	if err != nil {
		return nil, err
	}
	if status == http.StatusOK {
		return body, nil
	}

	// Most likely a stale buildId after a deploy — refresh once and retry.
	id, err = currentBuildID.Get(true)
	if err != nil {
		return nil, errors.Wrap(err, "failed to refresh buildId")
	}
	body, status, err = fetchEditionJSON(id, date, slug)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, errors.Errorf("edition request failed with status %d", status)
	}
	return body, nil
}

func fetchEditionJSON(buildID, date, slug string) ([]byte, int, error) {
	url := fmt.Sprintf("%s/_next/data/%s/edition/%s.json?date=%s&page=1&city=%s",
		config.BaseURL, buildID, slug, date, slug)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, 0, errors.Wrap(err, "failed to create new request")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Referer", config.BaseURL+"/")
	req.Header.Set("x-nextjs-data", "1")
	req.Header.Set("Connection", "keep-alive")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, errors.Wrap(err, "http request failed")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, errors.Wrap(err, "failed to read response body")
	}
	return body, resp.StatusCode, nil
}

// GetPages parses the Next.js data payload and returns the edition's
// pages sorted in reading order.
func GetPages(b []byte) ([]EpaperPage, error) {
	var nd NextPageData
	if err := json.Unmarshal(b, &nd); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal JSON")
	}
	pages := nd.PageProps.Edition.Pages
	if len(pages) == 0 {
		return nil, errors.New("no pages found in response")
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].PageNumber < pages[j].PageNumber })
	for _, p := range pages {
		fmt.Println(p.ViewerSrc)
	}
	return pages, nil
}

// =====================================================================
// Image download + PDF assembly
// =====================================================================

// downloadPageImage downloads a page's viewerSrc and re-encodes it as
// JPEG, which gopdf embeds efficiently (via DCTDecode, keeping the JPEG
// compression intact) — PNG was producing enormous PDFs for what is
// essentially photo/text scan content.
//
// The URL always ends in .webp, but the image host does content
// negotiation off the Accept header — it can hand back JPEG, PNG, WebP,
// or (if asked) AVIF, regardless of the .webp extension. We deliberately
// do NOT list image/avif: Go's standard image decoders can't read AVIF
// (it's AV1-based, not something image/jpeg, image/png, or
// golang.org/x/image/webp understand), so asking for it just produces an
// "unknown format" error even though the bytes are perfectly valid
// images. Instead we ask for formats we can actually decode, and — belt
// and braces — don't assume the response matches what we asked for:
// image.Decode sniffs the real bytes and picks whichever of the
// registered decoders (webp/jpeg/png) matches.
func downloadPageImage(url, filename string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to create request for %s", url)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", config.BaseURL+"/")
	req.Header.Set("Accept", "image/webp,image/png,image/jpeg;q=0.9,*/*;q=0.5")

	resp, err := httpClient.Do(req)
	if err != nil {
		return errors.Wrapf(err, "failed to get image from url %s", url)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrapf(err, "failed to read image body from %s", url)
	}

	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("http request failed with status %d from url %s (content-type %q): %s",
			resp.StatusCode, url, resp.Header.Get("Content-Type"), bodySnippet(body))
	}

	// ISOBMFF-family formats (AVIF, HEIC/HEIF) share a "....ftyp<brand>"
	// header and aren't decodable by Go's standard image packages. Flag
	// this explicitly rather than letting it fall through to a generic
	// "unknown format" error, since it means the CDN served a format we
	// didn't ask for despite the Accept header.
	if len(body) >= 12 && string(body[4:8]) == "ftyp" {
		brand := strings.TrimRight(string(body[8:12]), "\x00")
		return errors.Errorf(
			"got an ISOBMFF/%s image (likely AVIF or HEIC) for %s despite requesting webp/png/jpeg — "+
				"the CDN ignored the Accept header for this asset (content-type %q)",
			brand, url, resp.Header.Get("Content-Type"))
	}

	img, format, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return errors.Wrapf(err, "failed to decode image %s (content-type %q, %d bytes, body starts %s)",
			url, resp.Header.Get("Content-Type"), len(body), bodySnippet(body))
	}
	_ = format // "webp", "jpeg", or "png" — not needed beyond diagnostics

	// Decoded images can come back in various color models (YCbCr, NRGBA,
	// paletted, ...) depending on source format; normalize to a concrete
	// 8-bit RGBA raster before encoding so the result is consistent
	// regardless of what was actually downloaded.
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)

	file, err := os.Create(filename)
	if err != nil {
		return errors.Wrapf(err, "failed to create file %s", filename)
	}
	defer file.Close()

	if err := jpeg.Encode(file, rgba, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return errors.Wrapf(err, "failed to encode jpeg %s", filename)
	}
	return nil
}

// bodySnippet returns a short, safely-printable preview of a response
// body — enough to tell at a glance whether a failed download actually
// came back as an HTML block/challenge page rather than image bytes.
func bodySnippet(b []byte) string {
	const max = 200
	if len(b) > max {
		b = b[:max]
	}
	return strconv.Quote(string(b))
}

func DownloadImages(pages []EpaperPage, slug, date string) ([]string, error) {
	var wg sync.WaitGroup
	errChan := make(chan error, len(pages))

	dir := fmt.Sprintf("./%s-%s", slug, date)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, errors.Wrap(err, "failed to create directory")
	}

	// Worker pool to cap concurrent downloads at config.MaxDownloads.
	imageJobs := make(chan struct{}, config.MaxDownloads)
	filenames := make([]string, len(pages))

	for i, page := range pages {
		wg.Add(1)
		go func(i int, page EpaperPage) {
			defer wg.Done()

			filename := fmt.Sprintf("%s/page_%d.jpg", dir, i)
			filenames[i] = filename
			if _, err := os.Stat(filename); err == nil {
				fmt.Println("File exists already, skipping download")
				return
			}

			imageJobs <- struct{}{}
			err := downloadPageImage(page.ViewerSrc, filename)
			<-imageJobs

			if err != nil {
				errChan <- errors.Wrapf(err, "failed to download page %d", page.PageNumber)
			}
		}(i, page)
	}
	wg.Wait()
	close(errChan)

	var combinedErr error
	for err := range errChan {
		if combinedErr == nil {
			combinedErr = err
		} else {
			combinedErr = errors.Wrap(combinedErr, err.Error())
		}
	}
	if combinedErr != nil {
		return nil, combinedErr
	}
	return filenames, nil
}

// pdfMarginPt is a blank border added around each page image. The page
// size used to match the image exactly with zero border, so any viewer
// or printer that enforces its own non-zero minimum margin ended up
// clipping a sliver of the actual scan off the edge. ~1cm of margin (the
// image itself isn't scaled, just given more room on the page) avoids
// that entirely.
const pdfMarginPt = 28.35 // ~1cm (72pt/in ÷ 2.54cm/in)

// CreatePdf builds a single PDF from the downloaded page images, sizing
// each PDF page to its source image's own dimensions (known up front
// from ViewerWidth/ViewerHeight) plus pdfMarginPt of border on every
// side, so nothing gets clipped or stretched.
func CreatePdf(imgs []string, pages []EpaperPage, outputFileName string) error {
	if _, err := os.Stat(outputFileName); err == nil {
		fmt.Println("File exists")
		return nil
	}

	pdf := gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: gopdf.Rect{
		W: 1030 + 2*pdfMarginPt,
		H: 1680 + 2*pdfMarginPt,
	}})

	for i, imgPath := range imgs {
		w, h := 1030.0, 1680.0
		if i < len(pages) && pages[i].ViewerWidth > 0 && pages[i].ViewerHeight > 0 {
			w = float64(pages[i].ViewerWidth)
			h = float64(pages[i].ViewerHeight)
		}
		pageW, pageH := w+2*pdfMarginPt, h+2*pdfMarginPt

		pdf.AddPageWithOption(gopdf.PageOption{PageSize: &gopdf.Rect{W: pageW, H: pageH}})
		if err := pdf.Image(imgPath, pdfMarginPt, pdfMarginPt, &gopdf.Rect{W: w, H: h}); err != nil {
			return errors.Wrapf(err, "failed to add image %s", imgPath)
		}
	}
	return pdf.WritePdf(outputFileName)
}

// =====================================================================
// HTTP server
// =====================================================================

func main() {
	go func() {
		for {
			resp, err := http.Get("https://news-paper-ssa9.onrender.com/health")
			if err != nil {
				log.Println(err)
				continue
			}
			resp.Body.Close()
			log.Printf("Health check sent at %v", time.Now().Format(time.RFC3339))
			time.Sleep(5 * time.Minute)
		}
	}()

	r := gin.Default()
	TemplateHttp(r)
	r.GET("/", TemplateBasic())
	r.GET("/images/:edition", TemplateImages())
	r.GET("/health", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.Run(":8080")
}

func TemplateHttp(r *gin.Engine) {
	files := []string{
		"./templates/index.html",
		"./templates/image.html",
		"./templates/global/base.html",
		"./templates/global/footer.html",
		"./templates/global/js.html",
		"./templates/global/navbar.html",
	}
	r.LoadHTMLFiles(files...)
	r.StaticFS("/static", http.Dir("./static"))

	r.GET("ePaper/:edition", func(c *gin.Context) {
		date := time.Now().In(time.FixedZone("IST", 19800)).Format("2006-01-02")
		if qd := c.Query("date"); qd != "" {
			date = qd
		}
		edition := c.Param("edition")

		pdfPath, err := processEpaper(date, edition)
		if err != nil {
			c.String(http.StatusInternalServerError, fmt.Sprintf("Error processing ePaper: %v", err))
			return
		}
		c.FileAttachment(pdfPath, pdfPath)
	})
}

func processEpaper(date, editionInput string) (string, error) {
	e, err := editions.Resolve(editionInput)
	if err != nil {
		return "", errors.Wrap(err, "failed to resolve edition")
	}
	slug := e.Slug()

	data, err := GetData(date, slug)
	if err != nil {
		return "", errors.Wrap(err, "failed to get data")
	}
	pages, err := GetPages(data)
	if err != nil {
		return "", errors.Wrap(err, "failed to get pages")
	}

	filenames, err := DownloadImages(pages, slug, date)
	if err != nil {
		return "", errors.Wrap(err, "failed to download images")
	}

	filename := fmt.Sprintf("./%s-%s", slug, date)
	outputName := fmt.Sprintf("%s.pdf", filename)
	if err := CreatePdf(filenames, pages, outputName); err != nil {
		return "", errors.Wrap(err, "failed to create PDF")
	}

	return outputName, nil
}

type TemplateData struct {
	Title       string
	Body        string
	Options     jsondata.AutoGenerated
	Images      []string
	EditionID   string
	EditionName string
}

func TemplateBasic() gin.HandlerFunc {
	return func(c *gin.Context) {
		var ag jsondata.AutoGenerated
		err := json.Unmarshal([]byte(jsondata.JD), &ag)
		if err != nil {
			log.Fatal(err)
		}

		data := TemplateData{
			Title:   "Hindustan Epaper download for today!",
			Body:    "This is a test",
			Options: ag,
		}
		c.HTML(http.StatusOK, "index.html", data)
	}
}

func TemplateImages() gin.HandlerFunc {
	return func(c *gin.Context) {
		date := time.Now().In(time.FixedZone("IST", 19800)).Format("2006-01-02")
		if qd := c.Query("date"); qd != "" {
			date = qd
		}
		editionInput := c.Param("edition")

		e, err := editions.Resolve(editionInput)
		if err != nil {
			c.String(http.StatusBadRequest, fmt.Sprintf("Unknown edition %q: %v", editionInput, err))
			return
		}
		slug := e.Slug()

		data, err := GetData(date, slug)
		if err != nil {
			c.String(http.StatusInternalServerError, fmt.Sprintf("Error processing ePaper: %v", err))
			return
		}
		pages, err := GetPages(data)
		if err != nil {
			c.String(http.StatusInternalServerError, fmt.Sprintf("Error processing ePaper: %v", err))
			return
		}

		// viewerSrc is already a plain, directly-viewable URL — no
		// decryption step is needed to build the gallery any more.
		imagesString := make([]string, len(pages))
		for i, page := range pages {
			imagesString[i] = page.ViewerSrc
		}

		data2 := TemplateData{
			EditionName: e.EditionDisplayName,
			EditionID:   e.EditionCode,
			Body:        "Here are the images for today's ePaper",
			Options:     jsondata.AutoGenerated{},
			Images:      imagesString,
		}
		c.HTML(http.StatusOK, "image.html", data2)
	}
}
