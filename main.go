package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/signintech/gopdf"
)

func GetData(now string, edition string) ([]byte, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://epaper.livehindustan.com/Home/GetAllpages?editionid="+edition+"&editiondate="+now, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/115.0")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	// req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("DNT", "1")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Referer", "https://epaper.livehindustan.com/hazaribagh?eddate=11/02/2024")
	req.Header.Set("Cookie", "GDPR_COOKIE_LAW_CONSENT=true; _coach_tour=done; AWSALB=aGpOinzOWkHt8bo+VNZfhRBknIuQBmps8ThGwznt6Q3zrbS7NVEIbdaFeutFP/S5KNW/BEkUWqs9yo4lKClj7GUtLZpoSD72bXHh3O+96q21u9ZCi91xnA4rBPk+; AWSALBCORS=aGpOinzOWkHt8bo+VNZfhRBknIuQBmps8ThGwznt6Q3zrbS7NVEIbdaFeutFP/S5KNW/BEkUWqs9yo4lKClj7GUtLZpoSD72bXHh3O+96q21u9ZCi91xnA4rBPk+; ASP.NET_SessionId=drkqjvrmnoadnqzminxunymo; ViewType=ViewType=3; type_of_plateform=2; subTokenStatus=EbJgK+kpqJeqiFW3OuYMuzpZQWq9qsCqVQzTS2fxAXe1h8URCLFYupf/v9YKcV5RuR6A9WWhTu372YKbjlycsQ==; PageIdBeforePaywallVisible=; theme=theme-day; Home=1; changeddate=11/02/2024; PageName=01%3A%20FRONT%20PAGE; homelocation=notset; PageId=; MainEditionId=1017; EditionId=1017; editionCode_=Hazaribagh; MainEdName=%E0%A4%B9%E0%A4%9C%E0%A4%BE%E0%A4%B0%E0%A5%80%E0%A4%AC%E0%A4%BE%E0%A4%97; mintMainEditionName=%E0%A4%B9%E0%A4%9C%E0%A4%BE%E0%A4%B0%E0%A5%80%E0%A4%AC%E0%A4%BE%E0%A4%97")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("TE", "trailers")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bodyText, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// fmt.Printf("%s\n", bodyText)
	return bodyText, nil
}

type Page struct {
	HighResolution string `json:"HighResolution"`
}

func GetImages(b []byte) ([]Page, error) {
	var pages []Page
	if err := json.Unmarshal(b, &pages); err != nil {
		return nil, err
	}

	for _, page := range pages {
		fmt.Println(page.HighResolution)
	}
	return pages, nil
}

func main() {
	// data, err := GetData(now)
	// if err != nil {
	// 	fmt.Println(err)
	// 	return
	// }
	// images, err := GetImages(data)
	// if err != nil {
	// 	fmt.Println(err)
	// 	return
	// }
	// filenames, err := DownloadImages(images)
	// if err != nil {
	// 	fmt.Println(err)
	// 	return
	// }
	// err = CreatePdf(filenames, "output.pdf")
	// if err != nil {
	// 	fmt.Println(err)
	// 	return
	// }
	r := gin.Default()
	TemplateHttp(r)
	r.GET("/", TemplateBasic())
	r.Run(":8080")
}

func TemplateHttp(r *gin.Engine) {
	files := []string{
		"./templates/index.html",
		"./templates/global/base.html",
		"./templates/global/footer.html",
		"./templates/global/js.html",
		"./templates/global/navbar.html",
	}
	r.LoadHTMLFiles(files...)
	r.StaticFS("/static", http.Dir("./static"))

	r.GET("ePaper/:edition", func(c *gin.Context) {
		now := time.Now().In(time.FixedZone("IST", 19800)).Format("02/01/2006")
		edition := c.Param("edition")
		fmt.Println(now, edition)

		// date := c.Param("date")
		data, err := GetData(now, edition)
		if err != nil {
			fmt.Println(err)
			return
		}
		images, err := GetImages(data)
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println("gentImage complete")
		filenames, err := DownloadImages(images, edition, now)
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println("download image complete")

		filename := fmt.Sprintf("./%s-%s", edition, strings.Join(strings.Split(now, "/"), "-"))
		outputName := fmt.Sprintf("%s.pdf", filename)
		err = CreatePdf(filenames, outputName)
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println("create pdf complete complete")

		c.FileAttachment(outputName, outputName)
		fmt.Println("attached")
	})

}

type TemplateData struct {
	Title   string
	Body    string
	Options map[string]string
}

type Config struct {
	TemplateData
}

func TemplateBasic() gin.HandlerFunc {
	fmt.Println("adsadsa")
	return func(c *gin.Context) {
		fmt.Println("hererere")
		data := TemplateData{
			Title: "Hello, World!",
			Body:  "This is a test",
			Options: map[string]string{
				"1017": "Hazaribag",
				"1019": "Patna",
				// "3":    "three",
			},
		}
		c.HTML(http.StatusOK, "index.html", data)
	}
}

// 1824 × 2958 pixels
func CreatePdf(imgs []string, outputFileName string) error {
	// if outputname of file exists then do not create this step
	if _, err := os.Stat(outputFileName); err == nil {
		fmt.Println("File exists")
		return nil
	}
	pdf := gopdf.GoPdf{}
	size := gopdf.Rect{}
	// size.PointsToUnits(gopdf.Unit_IN)
	size.W = 1030
	size.H = 1680
	// leftMargin := 50.0
	// leftMarginPoints := leftMargin * 72.0 / 25.4 // Convert mm to points
	pdf.Start(gopdf.Config{PageSize: size})
	for _, imgPath := range imgs {
		pdf.AddPage()
		err := pdf.Image(imgPath, 2, 0, nil)
		if err != nil {
			return err
		}
	}
	return pdf.WritePdf(outputFileName)
}

func DownloadImages(pages []Page, editionNo, date string) ([]string, error) {
	filenames := []string{}
	for i, page := range pages {
		// edit page.HighResolution and remove
		// https://epsfs.hindustantimes.com/LH/2024/02/11/RANxPLMU/5_16/d60c92d0_16_mr.jpg
		// filename := fmt.Sprintf("./%s-%s/page_%d.jpg", editionNo, strings.Join(strings.Split(date, "/"), "-"), i)

		dir := fmt.Sprintf("./%s-%s", editionNo, strings.Join(strings.Split(date, "/"), "-"))
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return nil, err
		}

		filename := fmt.Sprintf("%s/page_%d.jpg", dir, i)
		filenames = append(filenames, filename)
		if _, err := os.Stat(filename); err == nil {
			fmt.Println("File exists already, skipping download")
			continue
		}
		dURL := editHighResolution(page.HighResolution)
		resp, err := http.Get(dURL)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		// file name is last part of the url
		file, err := os.Create(filename)
		if err != nil {
			return nil, err
		}
		defer file.Close()

		_, err = io.Copy(file, resp.Body)
		if err != nil {
			return nil, err
		}
	}
	return filenames, nil
}

func editHighResolution(url string) string {
	// Check if the URL has "_mr" in it
	if strings.Contains(url, "_mr") {
		// Replace "_mr" with an empty string
		return strings.Replace(url, "_mr", "", -1)
	}
	// If the URL doesn't contain "_mr", return it as is
	return url
}
