Output gif/rewrite.gif

Require butterfish

Set FontSize 18
Set Width 1000
Set Height 500
Set Framerate 30
Set PlaybackSpeed 1
Set Padding 10

Hide Sleep 2s Show

Type "butterfish rewrite -i ../Makefile -I 'Add an install command to this Makefile that copies ./bin/butterfish to ~/bin/butterfish. This should depend on ./bin/butterfish'"
Sleep 1s
Enter
Sleep 5s
Type "git diff ../Makefile" Enter

Sleep 30s
