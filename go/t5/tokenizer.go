package t5

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
)

// Given a filesystem path, load the file as JSON and mashal it into
// a TokenizerConfig.
func LoadTokenizerConfig(path string) *TokenizerConfig {
	file, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	config := &TokenizerConfig{}
	err = decoder.Decode(config)
	if err != nil {
		panic(err)
	}
	return config
}

type CharTrie struct {
	root *CharTrieNode
}

type CharTrieNode struct {
	isLeaf   bool
	children map[rune]*CharTrieNode
}

func NewCharTrie() *CharTrie {
	return &CharTrie{
		root: defaultCharTrieNode(),
	}
}

func (ct *CharTrie) Push(text string) {
	node := ct.root
	for _, ch := range text {
		child := node.children[ch]
		if child == nil {
			child = defaultCharTrieNode()
			node.children[ch] = child
		}
		node = child
	}
	node.isLeaf = true
}

func (ct *CharTrie) CommonPrefixSearch(text string) []string {
	node := ct.root
	prefix := []rune{}
	r := []string{}

	for _, ch := range text {
		prefix = append(prefix, ch)
		node = node.children[ch]
		if node != nil && node.isLeaf {
			r = append(r, string(prefix))
		}
		if node == nil {
			break
		}
	}
	return r
}

func defaultCharTrieNode() *CharTrieNode {
	return &CharTrieNode{
		false,
		make(map[rune]*CharTrieNode),
	}
}

type TokenLattice struct {
	sentence   string
	len        int
	bosTokenId int
	eosTokenId int
	nodes      []*TokenLatticeNode
	beginNodes [][]*TokenLatticeNode
	endNodes   [][]*TokenLatticeNode
}

type TokenLatticeNode struct {
	TokenId        int
	NodeId         int
	Pos            int
	Length         int
	Score          float64
	Prev           *TokenLatticeNode
	BacktraceScore float64
}

func (node *TokenLatticeNode) Clone() *TokenLatticeNode {
	n := &TokenLatticeNode{
		TokenId:        node.TokenId,
		NodeId:         node.NodeId,
		Pos:            node.Pos,
		Length:         node.Length,
		Score:          node.Score,
		BacktraceScore: node.BacktraceScore,
	}
	n.Prev = node.Prev
	return n
}

func NewTokenLattice(sentence string, bosTokenId int, eosTokenId int) *TokenLattice {

	tl := &TokenLattice{
		sentence:   sentence,
		len:        len(sentence),
		bosTokenId: bosTokenId,
		eosTokenId: eosTokenId,
		beginNodes: make([][]*TokenLatticeNode, len(sentence)+1),
		endNodes:   make([][]*TokenLatticeNode, len(sentence)+1),
	}

	for i := 0; i < tl.len+1; i++ {
		tl.beginNodes[i] = make([]*TokenLatticeNode, 0)
		tl.endNodes[i] = make([]*TokenLatticeNode, 0)
	}
	bos := &TokenLatticeNode{bosTokenId, 0, 0, 0, 0.0, nil, 0.0}
	eos := &TokenLatticeNode{eosTokenId, 1, tl.len, 0, 0.0, nil, 0.0}
	tl.nodes = append(tl.nodes, bos.Clone())
	tl.nodes = append(tl.nodes, eos.Clone())
	tl.beginNodes[tl.len] = append(tl.beginNodes[tl.len], eos)
	tl.endNodes[0] = append(tl.endNodes[0], bos)

	return tl
}

func (this *TokenLattice) GetSentence() string {
	return this.sentence
}

func (this *TokenLattice) Insert(pos int, length int, score float64, tokenId int) {
	nodeId := len(this.nodes)
	node := &TokenLatticeNode{tokenId, nodeId, pos, length, score, nil, 0.0}
	this.beginNodes[pos] = append(this.beginNodes[pos], node)
	this.endNodes[pos+length] = append(this.endNodes[pos+length], node)
	this.nodes = append(this.nodes, node)
}

func (this *TokenLattice) viterbi() []*TokenLatticeNode {
	length := this.len
	pos := 0
	for pos <= length {
		if len(this.beginNodes[pos]) == 0 {
			return make([]*TokenLatticeNode, 0)
		}
		for _, rnode := range this.beginNodes[pos] {
			rnode.Prev = nil
			bestScore := 0.0
			var bestNode *TokenLatticeNode

			for _, lnode := range this.endNodes[pos] {
				score := lnode.BacktraceScore + rnode.Score
				if bestNode == nil || score > bestScore {
					bestNode = lnode.Clone()
					bestScore = score
				}
			}
			if bestNode != nil {
				rnode.Prev = bestNode
				rnode.BacktraceScore = bestScore
			} else {
				return make([]*TokenLatticeNode, 0)
			}
		}
		pos++
	}
	results := make([]*TokenLatticeNode, 0)
	root := this.beginNodes[length][0]
	prev := root.Prev
	if prev == nil {
		return make([]*TokenLatticeNode, 0)
	}
	node := prev.Clone()
	for node.Prev != nil {
		results = append(results, node.Clone())
		n := node.Clone()
		node = n.Prev.Clone()
	}
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results
}

func (this *TokenLattice) piece(node *TokenLatticeNode) string {
	return this.sentence[node.Pos : node.Pos+node.Length]
}

func (this *TokenLattice) Tokens() []string {
	nodes := this.viterbi()
	result := make([]string, 0)
	for _, n := range nodes {
		result = append(result, this.piece(n))
	}
	return result
}

func (this *TokenLattice) TokenIds() []int {
	nodes := this.viterbi()
	tokenIds := make([]int, 0)
	for _, n := range nodes {
		tokenIds = append(tokenIds, n.TokenId)
	}
	return tokenIds
}

type PatternConfig struct {
	Regex string
}

type NormalizersConfig struct {
	Type                string
	PrecompiledCharsmap string `json:"precompiled_charsmap"`
	Pattern             PatternConfig
	Content             string
	Normalizers         []NormalizersConfig
}

type SubTokenizerConfig struct {
	Type           string
	Replacement    string
	AddPrefixSpace bool `json:"add_prefix_space"`
	Normalizers    []NormalizersConfig
}

type Gram struct {
	Word  string
	Score float64
}

type TokenizerModelConfig struct {
	Type  string
	UnkId int `json:"unk_id"`
	Vocab [][]any
}

type AddedToken struct {
	Id         int
	Content    string
	SingleWord bool `json:"single_word"`
	LStrip     bool
	RStrip     bool
	Normalized bool
	Special    bool
}

type TokenizerConfig struct {
	Normalizer   *NormalizersConfig
	PreTokenizer *SubTokenizerConfig `json:"pre_tokenizer"`
	Decoder      *SubTokenizerConfig
	Model        *TokenizerModelConfig
	AddedTokens  []*AddedToken `json:"added_tokens"`
}

type Tokenizer struct {
	vocab           []Gram
	unkTokenId      int
	specialTokens   []*AddedToken
	specialTokenIds map[int]string
	normalizer      Normalizer
	preTokenizer    Pretokenizer
	decoder         Decoder
	tokenToIds      map[string]int
	bosToken        string
	bosTokenId      int
	eosToken        string
	eosTokenId      int
	unkToken        string
	trie            *CharTrie
	minScore        float64
	unkScore        float64
}

func vocabToGrams(vocab [][]any) []Gram {
	grams := make([]Gram, 0)
	for _, v := range vocab {
		if len(v) != 2 {
			panic("invalid vocab")
		}
		gram := Gram{v[0].(string), v[1].(float64)}
		grams = append(grams, gram)
	}
	return grams
}

func NewTokenizer(config *TokenizerConfig) *Tokenizer {
	preTokenizer := PreTokenizerFromConfig(config.PreTokenizer)
	normalizer := NormalizerFromConfig(config.Normalizer)
	decoder := DecoderFromConfig(config.Decoder)

	tokenizer := &Tokenizer{
		vocab:         vocabToGrams(config.Model.Vocab),
		unkTokenId:    config.Model.UnkId,
		specialTokens: config.AddedTokens,
		normalizer:    normalizer,
		preTokenizer:  preTokenizer,
		decoder:       decoder,
		tokenToIds:    make(map[string]int),
		trie:          NewCharTrie(),
		minScore:      1.0e6,
	}

	for i, gram := range tokenizer.vocab {
		tokenizer.tokenToIds[tokenizer.Normalize(gram.Word)] = i
		tokenizer.minScore = math.Min(tokenizer.minScore, gram.Score)
		tokenizer.trie.Push(gram.Word)
	}

	tokenizer.unkScore = tokenizer.minScore - 10.0
	tokenizer.vocab[tokenizer.unkTokenId].Score = tokenizer.unkScore

	tokenizer.bosToken = tokenizer.Normalize(" ")
	tokenizer.bosTokenId = tokenizer.GetTokenId(tokenizer.bosToken)
	tokenizer.eosToken = "</s>"
	tokenizer.eosTokenId = tokenizer.GetTokenId(tokenizer.eosToken)
	tokenizer.unkToken = tokenizer.vocab[tokenizer.unkTokenId].Word

	tokenizer.specialTokenIds = make(map[int]string)
	for _, token := range tokenizer.specialTokens {
		tokenizer.specialTokenIds[token.Id] = token.Content
	}

	return tokenizer
}

func (this *Tokenizer) GetTokenId(normalizedToken string) int {
	return this.tokenToIds[normalizedToken]
}

func (this *Tokenizer) Normalize(text string) string {
	return this.normalizer.Normalize(text)
}

func (this *Tokenizer) PreTokenize(normalized []string) []string {
	return this.preTokenizer.PreTokenize(normalized)
}

func (this *Tokenizer) PopulateNodes(lattice *TokenLattice) {
	sentence := lattice.GetSentence()
	length := len(sentence)
	beginPos := 0
	for beginPos < length {
		mblen := 1
		hasSingleNode := false
		tokens := []string{}

		for _, token := range this.trie.CommonPrefixSearch(sentence[beginPos:]) {
			tokens = append(tokens, token)
			tokenId := this.GetTokenId(token)
			tokenScore := this.vocab[tokenId].Score
			n := len(token)
			lattice.Insert(beginPos, n, tokenScore, tokenId)
			if !hasSingleNode && n == mblen {
				hasSingleNode = true
			}
		}
		if !hasSingleNode {
			lattice.Insert(beginPos, mblen, this.unkScore, this.unkTokenId)
		}
		beginPos += mblen
	}
}

func (this *Tokenizer) Tokenize(normalized string) []int {
	lattice := NewTokenLattice(normalized, this.bosTokenId, this.eosTokenId)
	this.PopulateNodes(lattice)
	return lattice.TokenIds()
}

func (this *Tokenizer) Encode(text string) []int {
	if text == "" {
		return []int{this.eosTokenId}
	}

	normalized := this.Normalize(text)
	pre := this.PreTokenize([]string{normalized})
	tokens := []int{}
	for _, token := range pre {
		tokenized := this.Tokenize(token)
		tokens = append(tokens, tokenized...)
	}
	tokens = append(tokens, this.eosTokenId)
	return tokens
}

func (this *Tokenizer) Decode(tokenIds []int, skipSpecialTokens bool) string {
	tokens := make([]string, len(tokenIds))
	for i, x := range tokenIds {
		if _, ok := this.specialTokenIds[x]; ok && skipSpecialTokens {
			tokens[i] = ""
		} else if x == this.unkTokenId {
			tokens[i] = this.unkToken + " "
		} else if x < len(this.vocab) {
			tokens[i] = this.vocab[x].Word
		} else {
			tokens[i] = fmt.Sprintf("[%d]", x)
		}
	}
	decodedTokens := this.decoder.DecodeChain(tokens)
	decoded := strings.Join(decodedTokens, "")
	return decoded
}

/////////////////////////////////////////////////////////////

type MetaspaceTokenProcessor struct {
	addPrefixSpace bool
	replacement    string
	strRep         string
}

func (m *MetaspaceTokenProcessor) PreTokenize(normalizedTokens []string) []string {
	result := []string{}
	for _, token := range normalizedTokens {
		normalized := strings.Replace(token, " ", m.strRep, -1)
		if m.addPrefixSpace && !strings.HasPrefix(normalized, m.replacement) {
			normalized = m.strRep + normalized
		}
		result = append(result, normalized)
	}
	return result
}

func (m *MetaspaceTokenProcessor) DecodeChain(tokens []string) []string {
	result := []string{}
	i := 0
	for _, token := range tokens {
		normalized := strings.Replace(token, m.replacement, " ", -1)
		if m.addPrefixSpace && i == 0 && strings.HasPrefix(normalized, " ") {
			normalized = normalized[1:]
		}
		result = append(result, normalized)
		i++
	}
	return result
}

type PrecompiledTokenProcessor struct {
	charsmap string
}

func (p PrecompiledTokenProcessor) Normalize(text string) string {
	return text
}

type ReplaceTokenProcessor struct {
	pattern string
	content string
	regex   *regexp.Regexp
}

func (this *ReplaceTokenProcessor) Normalize(text string) string {
	return this.regex.ReplaceAllString(text, this.content)
}

type SequencePreTokenizer struct {
	tokenizers []Pretokenizer
}

func (s SequencePreTokenizer) PreTokenize(normalizedTokens []string) []string {
	result := normalizedTokens
	for _, tokenizer := range s.tokenizers {
		result = tokenizer.PreTokenize(result)
	}
	return result
}

type SequenceNormalizer struct {
	normalizers []Normalizer
}

func (this SequenceNormalizer) Normalize(n string) string {
	s := n

	for _, tokenizer := range this.normalizers {
		s = tokenizer.Normalize(s)
	}
	return s
}

type WhitespaceSplitTokenProcessor struct{}

func (w *WhitespaceSplitTokenProcessor) PreTokenize(normalizedTokens []string) []string {
	var result []string
	for _, token := range normalizedTokens {
		result = append(result, strings.Fields(token)...)
	}
	return result
}

type Pretokenizer interface {
	PreTokenize(normalizedTokens []string) []string
}

type Normalizer interface {
	Normalize(text string) string
}

type Decoder interface {
	DecodeChain(tokens []string) []string
}

type DecoderPretokenizer interface {
	Decoder
	Pretokenizer
}

func PreTokenizerFromConfig(config *SubTokenizerConfig) Pretokenizer {
	switch config.Type {
	case "Metaspace":
		return &MetaspaceTokenProcessor{
			addPrefixSpace: config.AddPrefixSpace,
			replacement:    config.Replacement,
			strRep:         config.Replacement,
		}
	default:
		panic("Unknown pretokenizer type")
	}
}

func NormalizerFromConfig(config *NormalizersConfig) Normalizer {
	switch config.Type {
	case "Sequence":
		normalizers := []Normalizer{}

		for _, subConfig := range config.Normalizers {
			normalizers = append(normalizers, NormalizerFromConfig(&subConfig))
		}

		return &SequenceNormalizer{
			normalizers: normalizers,
		}

	case "Precompiled":
		return &PrecompiledTokenProcessor{
			charsmap: config.PrecompiledCharsmap,
		}

	case "Replace":
		return &ReplaceTokenProcessor{
			pattern: config.Pattern.Regex,
			content: config.Content,
			regex:   regexp.MustCompile(config.Pattern.Regex),
		}

	default:
		panic("Unknown normalizer type")
	}
}

func DecoderFromConfig(config *SubTokenizerConfig) Decoder {
	switch config.Type {
	case "Metaspace":
		return &MetaspaceTokenProcessor{
			addPrefixSpace: config.AddPrefixSpace,
			replacement:    config.Replacement,
			strRep:         config.Replacement,
		}
	default:
		panic("Unknown pretokenizer type")
	}
}
