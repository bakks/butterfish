#!/bin/bash

COMMIT=`git show --summary | head -n 1 | awk '{print $2}'`
DIRTY=`[[ $(git diff --shortstat 2> /dev/null | tail -n1) != "" ]] && echo "-dirty"`

echo $COMMIT$DIRTY
