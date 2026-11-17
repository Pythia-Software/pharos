# Spelling word list

`words.txt.gz` is the English word list the dictation estimate
(`internal/archive/dictation.go`) checks a person's words against to find
keyboard typos and ALL-CAPS emphasis. It is SCOWL (Spell Checker Oriented Word
Lists) release 2020.12.07, the source of Hunspell's `en_US` dictionary: its
english and american word and contraction lists up to size 60, lowercased,
about 101,000 words.

`COPYRIGHT` is SCOWL's copyright and permission notice, which must accompany
the list. The lists are free to use, copy, modify and distribute with that
notice.

To rebuild, download `scowl-2020.12.07.tar.gz` (SHA-256
`5587667caa20c4891390c2d42dbb4d5c4c3f41bee77af1457ece3ba23fb859cc`) from
<http://wordlist.aspell.net/>, extract it into an empty directory, and run
`python3 -I tools/spelling_words.py <extracted directory>`. Changing the list
changes the estimate: bump `dictationVersion` with it.
