package litevec

import (
	"bytes"
	"io"
	"math"
	"sort"
	str "strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/james-bowman/sparse"
	"github.com/jdkato/prose/tokenize"
	"gonum.org/v1/gonum/mat"
)

type Text []string
type Mapping map[string]mat.Vector

type Model struct {
	Mapping
	Rows mat.RowViewer
	mat.Matrix
}

func ReadText(raw io.Reader) (rtn Text, err error) {
	pipeline := []transform.Transformer{
		norm.NFD,
		runes.Remove(runes.In(unicode.Mn)),
		runes.Map(unicode.ToLower),
	}
	tx := transform.Chain(pipeline...)
	rd := transform.NewReader(raw, tx)
	var buf bytes.Buffer
	_, err = buf.ReadFrom(rd)
	if err != nil {
		return
	}
	return tokenize.TextToWords(buf.String()), nil
}

func MkText(s string) (rtn Text) {
	rtn, _ = ReadText(str.NewReader(s))
	return
}

type Doc struct {
	Tokens       Text
	TokenIndices map[string]int
}

func (D Doc) Vocab() (rtn []string) {
	rtn = make([]string, len(D.TokenIndices))
	for t, i := range D.TokenIndices {
		rtn[i] = t
	}
	return
}

func (D Doc) VocabLength() int {
	return len(D.TokenIndices)
}

func MkDoc(text Text) (rtn Doc) {
	rtn.Tokens = text
	i := 0
	for _, t := range text {
		if _, indexed := rtn.TokenIndices[t]; !indexed {
			rtn.TokenIndices[t] = i
			i++
		}
	}
	return
}

/// Returns an array of unigram probabilities indexed by token ID
func (D Doc) UnigramPs() (rtn []float64) {
	rtn = make([]float64, len(D.TokenIndices))
	for _, t := range D.Tokens {
		rtn[D.TokenIndices[t]]++
	}
	for i := 0; i < len(rtn); i++ {
		rtn[i] /= float64(len(rtn))
	}
	return
}

/// Returns an NxN matrix of co-occurrence values in the vocabulary.
func (D Doc) SkipgramPs(maxJuxt int) *sparse.CSR {
	n := D.VocabLength()
	s := n / 10
	rtn := sparse.NewCSR(n, n, make([]int, s), make([]int, s), make([]float64, s))
	for i := maxJuxt; i < len(D.Tokens)-maxJuxt-1; i++ {
		for j := 0; j < maxJuxt; j++ {
			a := D.Tokens[i]
			for _, b := range []string{D.Tokens[i+j], D.Tokens[i-j]} {
				a_i := D.TokenIndices[a]
				b_i := D.TokenIndices[b]
				displacement := math.Abs(float64(j))
				rtn.Set(a_i, b_i, rtn.At(a_i, b_i)+1/displacement)
			}
		}
	}
	rtn.DoNonZero(func(i, j int, v float64) {
		rtn.Set(i, j, rtn.At(i, j)/float64(len(D.Tokens)))
	})
	return rtn
}

/// Returns a normalized pointwise mutual information matrix.
func (D Doc) PMIs(maxJuxt int) (N *sparse.CSR) {
	U := D.UnigramPs()
	N = D.SkipgramPs(maxJuxt)
	// normalize
	N.DoNonZero(func(i, j int, v float64) {
		N.Set(i, j, math.Log(N.At(i, j)/(U[i]*U[j])))
	})
	return
}

func (D Doc) WordVecs(maxJuxt int, maxDim *int) (rtn Model) {
	svd := new(mat.SVD)
	sparse := D.PMIs(maxJuxt)
	svd.Factorize(sparse, mat.SVDFull)
	mat := svd.UTo(nil)
	rtn.Matrix = mat
	rtn.Rows = mat
	V := D.Vocab()
	for i := 0; i < len(V); i++ {
		rtn.Mapping[V[i]] = rtn.Rows.RowView(i)
	}
	return
}

func (m Mapping) Similarity(a, b string) float64 {
	return mat.Dot(m[a], m[b])
}

func (m Mapping) Vocab() (rtn Text) {
	for k, _ := range m {
		rtn = append(rtn, k)
	}
	return
}

func (m Mapping) Constellation(t string, n *int) Text {
	if n != nil {
		*n %= len(m)
	}
	V := m.Vocab()
	sort.Slice(V, func(i, j int) bool {
		return m.Similarity(t, V[i]) < m.Similarity(t, V[j])
	})
	return V[:*n]
}
