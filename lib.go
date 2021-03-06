package litevec

import (
	"bytes"
	"io"
	"math"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/james-bowman/sparse"
	"github.com/jdkato/prose/tokenize"
	"gonum.org/v1/gonum/mat"
)

type Text []string
type VecMapping map[string]mat.Vector

type Model struct {
	VecMapping
	Matrix *mat.Dense
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
	rtn, _ = ReadText(strings.NewReader(s))
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
		rtn.Set(i, j, v/float64(len(D.Tokens)))
	})
	return rtn
}

/// Returns a normalized pointwise mutual information matrix over the
/// co-occurrence probability of each term with each other
func (D Doc) PMIs(maxJuxt int) (N *sparse.CSR) {
	U := D.UnigramPs()
	N = D.SkipgramPs(maxJuxt)
	// normalize
	N.DoNonZero(func(i, j int, v float64) {
		N.Set(i, j, math.Log(v/(U[i]*U[j])))
	})
	return
}

func (D Doc) WordVecs(maxJuxt int, maxDim *int) (rtn Model) {
	svd := new(mat.SVD)
	sparse := D.PMIs(maxJuxt)
	svd.Factorize(sparse, mat.SVDFull)
	mat := svd.UTo(nil)
	rtn.Matrix = mat
	V := D.Vocab()
	for i := 0; i < len(V); i++ {
		rtn.VecMapping[V[i]] = rtn.Matrix.RowView(i)
	}
	return
}

func (m VecMapping) CosSim(a, b string) float64 {
	return mat.Dot(m[a], m[b])
}

func (m VecMapping) Vocab() (rtn Text) {
	for k, _ := range m {
		rtn = append(rtn, k)
	}
	return
}

func (m VecMapping) Embedding(src Text) (rtn *mat.VecDense) {
	rtn = mat.NewVecDense(len(m), []float64{})
	for _, t := range src {
		rtn.AddVec(rtn, m[t])
	}
	rtn.ScaleVec(1/float64(len(src)), rtn)
	return
}

func (m VecMapping) StrEmbedding(src string) *mat.VecDense {
	return m.Embedding(MkText(src))
}

/// Incidency can be thought of as the importance of a specific term to a given text:
/// Measuring the diversity of the contexts in which each term co-occurs yields a measurement of
/// how important it is to the document overall.
type Incidency map[string]float64

func (I Incidency) Of(D Doc, maxJuxt int) {
	I = make(Incidency, D.VocabLength())
	S := D.PMIs(maxJuxt)
	for t, i := range D.TokenIndices {
		var sigma float64
		S.DoRowNonZero(i, func(i, j int, v float64) {
			sigma += v
		})
		I[t] += sigma
	}
	for t, v := range I {
		// P(Q | T) = P(T | Q)^-1
		I[t] /= float64(len(I))
		I[t] = 1 / v
	}
}

func (I Incidency) Keywords(n *int) (rtn Text) {
	for t := range I {
		rtn = append(rtn, t)
	}
	sort.Slice(rtn, func(i, j int) bool {
		return I[rtn[i]] < I[rtn[j]]
	})
	if n != nil {
		k := *n
		k %= len(I)
		rtn = rtn[:k]
	}
	return
}

func (D Doc) KeyVecs(maxJuxt int, maxDim *int) (rtn VecMapping) {
	var I Incidency
	I.Of(D, maxJuxt)
	K := I.Keywords(maxDim)
	for _, k := range K {
		rtn[k] = D.WordVecs(maxJuxt, maxDim).VecMapping[k]
	}
	return
}

func (m VecMapping) Constellation(t string, n *int) Text {
	if n != nil {
		*n %= len(m)
	}
	V := m.Vocab()
	sort.Slice(V, func(i, j int) bool {
		return m.CosSim(t, V[i]) < m.CosSim(t, V[j])
	})
	return V[:*n]
}

type Adjacency map[string]float64

func (A Adjacency) Between(M ...VecMapping) {
	sf := float64(len(M[0]))
	for _, v := range M {
		sf = math.Max(float64(len(v)), sf)
	}
	s := int(sf)
	A = make(Adjacency, s)
	V := make(map[string]struct{}, s)
	for p, P := range M {
		for k := range P {
			ok := true
			Qs := append(M[:p], M[p+1:]...)
			for q := 0; q < len(Qs) && ok; q++ {
				_, ok = Qs[q][k]
			}
			if ok {
				V[k] = struct{}{}
			}
		}
	}
	for p, P := range M {
		for k := range V {
			A[k] = 0
			Qs := append(M[:p], M[p+1:]...)
			for q, Q := range Qs {
				var sigma float64
				for t := range V {
					// norm over the corporas' semantic similarity of k and t
					c := mat.Dot(P[k], Q[k])
					d := mat.Dot(P[t], Q[t])
					sigma += c / d
				}
				n := float64(q)
				sigma /= float64(len(V))
				A[k] = (A[k]*n + sigma) / n
			}
		}
	}
}

func (A Adjacency) DocSim() float64 {
	var sigma float64
	for _, x := range A {
		sigma += x
	}
	return sigma / float64(len(A))
}
