//  Copyright (c) 2014 Couchbase, Inc.
//  Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file
//  except in compliance with the License. You may obtain a copy of the License at
//    http://www.apache.org/licenses/LICENSE-2.0
//  Unless required by applicable law or agreed to in writing, software distributed under the
//  License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
//  either express or implied. See the License for the specific language governing permissions
//  and limitations under the License.

package main

import (
	"encoding/json"
	_ "expvar"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/blevesearch/bleve"
	bleveHttp "github.com/blevesearch/bleve/http"
)

var batchSize = flag.Int("batchSize", 100, "batch size for indexing")
var bindAddr = flag.String("addr", ":8094", "http listen address")
var jsonDir = flag.String("jsonDir", "data/", "json directory")
var indexPath = flag.String("index", "beer-search.bleve", "index path")
var staticEtag = flag.String("staticEtag", "", "A static etag value.")
var staticPath = flag.String("static", "static/", "Path to the static content")
var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")
var memprofile = flag.String("memprofile", "", "write mem profile to file")

func main() {

	flag.Parse()

	log.Printf("GOMAXPROCS: %d", runtime.GOMAXPROCS(-1))

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
	}

	// open the index
	beerIndex, err := bleve.Open(*indexPath)
	if err == bleve.ErrorIndexPathDoesNotExist {
		log.Printf("Creating new index...")
		// create a mapping
		indexMapping, err := buildIndexMapping()
		if err != nil {
			log.Fatal(err)
		}
		beerIndex, err = bleve.New(*indexPath, indexMapping)
		if err != nil {
			log.Fatal(err)
		}

		// index data in the background
		go func() {
			err = indexBeer(beerIndex)
			if err != nil {
				log.Fatal(err)
			}
			pprof.StopCPUProfile()
			if *memprofile != "" {
				f, err := os.Create(*memprofile)
				if err != nil {
					log.Fatal(err)
				}
				pprof.WriteHeapProfile(f)
				f.Close()
			}
		}()
	} else if err != nil {
		log.Fatal(err)
	} else {
		log.Printf("Opening existing index...")
	}

	// create a router to serve static files
	router := staticFileRouter()

	// add the API
	bleveHttp.RegisterIndexName("beer", beerIndex)
	searchHandler := bleveHttp.NewSearchHandler("beer")
	router.Handle("/api/search", searchHandler).Methods("POST")
	listFieldsHandler := bleveHttp.NewListFieldsHandler("beer")
	router.Handle("/api/fields", listFieldsHandler).Methods("GET")

	debugHandler := bleveHttp.NewDebugDocumentHandler("beer")
	debugHandler.DocIDLookup = docIDLookup
	router.Handle("/api/debug/{docID}", debugHandler).Methods("GET")

	// start the HTTP server
	http.Handle("/", router)
	log.Printf("Listening on %v", *bindAddr)
	log.Fatal(http.ListenAndServe(*bindAddr, nil))

}

func indexBeer(i bleve.Index) error {

	// open the directory
	dirEntries, err := ioutil.ReadDir(*jsonDir)
	if err != nil {
		return err
	}

	// walk the directory entries for indexing
	log.Printf("Indexing...")
	count := 0
	startTime := time.Now()
	batch := i.NewBatch()
	batchCount := 0
	for _, dirEntry := range dirEntries {
		filename := dirEntry.Name()
		// read the bytes
		jsonBytes, err := ioutil.ReadFile(*jsonDir + "/" + filename)
		if err != nil {
			return err
		}
		// parse bytes as json
		var jsonDoc interface{}
		err = json.Unmarshal(jsonBytes, &jsonDoc)
		if err != nil {
			return err
		}
		ext := filepath.Ext(filename)
		docID := filename[:(len(filename) - len(ext))]
		batch.Index(docID, jsonDoc)
		batchCount++

		if batchCount >= *batchSize {
			err = i.Batch(batch)
			if err != nil {
				return err
			}
			batch = i.NewBatch()
			batchCount = 0
		}
		count++
		if count%1000 == 0 {
			indexDuration := time.Since(startTime)
			indexDurationSeconds := float64(indexDuration) / float64(time.Second)
			timePerDoc := float64(indexDuration) / float64(count)
			log.Printf("Indexed %d documents, in %.2fs (average %.2fms/doc)", count, indexDurationSeconds, timePerDoc/float64(time.Millisecond))
		}
	}
	// flush the last batch
	if batchCount > 0 {
		err = i.Batch(batch)
		if err != nil {
			log.Fatal(err)
		}
	}
	indexDuration := time.Since(startTime)
	indexDurationSeconds := float64(indexDuration) / float64(time.Second)
	timePerDoc := float64(indexDuration) / float64(count)
	log.Printf("Indexed %d documents, in %.2fs (average %.2fms/doc)", count, indexDurationSeconds, timePerDoc/float64(time.Millisecond))
	return nil
}
