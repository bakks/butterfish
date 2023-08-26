Output gif/exec.gif

Set CursorBlink false
Require butterfish

Set FontSize 26
Set Padding 14
Set Theme "Monokai Pro"
Set PlaybackSpeed 1.0
Set Framerate 20
Set CursorBlink false

Hide

Type "cd ~/butterfish"
Enter
Type "clear"
Enter

Sleep 2s Show

Type "butterfish exec 'chmod Makefile 644'"
Sleep 1s
Enter
Sleep 10s
Type "y" Enter

Sleep 3s
Type "ls -l | grep Makefile"
Enter

Sleep 30s
