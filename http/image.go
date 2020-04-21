package http

import (
	iiifcache "github.com/go-iiif/go-iiif/v3/cache"
	iiifconfig "github.com/go-iiif/go-iiif/v3/config"
	iiifdriver "github.com/go-iiif/go-iiif/v3/driver"
	iiifimage "github.com/go-iiif/go-iiif/v3/image"
	iiiflevel "github.com/go-iiif/go-iiif/v3/level"
	iiifsource "github.com/go-iiif/go-iiif/v3/source"
	_ "log"
	gohttp "net/http"
	"sync/atomic"
	"time"
)

func ImageHandler(config *iiifconfig.Config, driver iiifdriver.Driver, images_cache iiifcache.Cache, derivatives_cache iiifcache.Cache) (gohttp.HandlerFunc, error) {

	fn := func(w gohttp.ResponseWriter, r *gohttp.Request) {

		/*

		   Okay, you see all of this? We're going to validate all the things including
		   a new transformation in order to be able to call ToURI() to account for the
		   fact that the "default" format is whatever the server wants it to be which
		   means we need to convert "default" into "color" or whatever in order for the
		   caching layer to work and find things generated by iiif-tile-seed. Good times...
		   (20160916/thisisaaronland)

		*/

		query, err := NewIIIFQueryParser(r)

		if err != nil {
			gohttp.Error(w, err.Error(), gohttp.StatusBadRequest)
			return
		}

		params, err := query.GetIIIFParameters()

		if err != nil {
			gohttp.Error(w, err.Error(), gohttp.StatusBadRequest)
			return
		}

		endpoint := EndpointFromRequest(r)
		level, err := iiiflevel.NewLevelFromConfig(config, endpoint)

		if err != nil {
			gohttp.Error(w, err.Error(), gohttp.StatusInternalServerError)
			return
		}

		transformation, err := iiifimage.NewTransformation(level, params.Region, params.Size, params.Rotation, params.Quality, params.Format)

		if err != nil {
			gohttp.Error(w, err.Error(), gohttp.StatusBadRequest)
			return
		}

		uri, err := transformation.ToURI(params.Identifier)

		if err != nil {
			gohttp.Error(w, err.Error(), gohttp.StatusBadRequest)
			return
		}

		body, err := derivatives_cache.Get(uri)

		if err == nil {

			cacheHit.Add(1)

			source, _ := iiifsource.NewMemorySource(body)
			image, _ := driver.NewImageFromConfigWithSource(config, source, "cache")

			w.Header().Set("Content-Type", image.ContentType())
			w.Write(image.Body())
			return
		}

		image, err := driver.NewImageFromConfigWithCache(config, images_cache, params.Identifier)

		if err != nil {
			gohttp.Error(w, err.Error(), gohttp.StatusInternalServerError)
			return
		}

		/*
			something something something maybe sendfile something something
			(20160901/thisisaaronland)
		*/

		if transformation.HasTransformation() {

			cacheMiss.Add(1)

			t1 := time.Now()
			err = image.Transform(transformation)
			t2 := time.Since(t1)

			if err != nil {
				gohttp.Error(w, err.Error(), gohttp.StatusInternalServerError)
				return
			}

			go func(t time.Duration) {

				ns := t.Nanoseconds()
				ms := ns / (int64(time.Millisecond) / int64(time.Nanosecond))

				timers_mu.Lock()

				counter := atomic.AddInt64(&transforms_counter, 1)
				timer := atomic.AddInt64(&transforms_timer, ms)

				avg := float64(timer) / float64(counter)

				transformsCount.Add(1)
				transformsAvgTime.Set(avg)

				timers_mu.Unlock()
			}(t2)

			go func(k string, im iiifimage.Image) {

				derivatives_cache.Set(k, im.Body())
				cacheSet.Add(1)

			}(uri, image)
		}

		w.Header().Set("Content-Type", image.ContentType())
		w.Write(image.Body())
		return
	}

	h := gohttp.HandlerFunc(fn)
	return h, nil
}
