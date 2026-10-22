set pagination off
break *0x451812
commands
silent
printf "FIRST rsi=%p\n", $rsi
x/16xb $rsi+0x10
continue
end
break *0x45190d
commands
silent
printf "SECOND rsi=%p\n", $rsi
x/16xb $rsi+0x10
continue
end
run -c /tmp/ajs-lab/ajs.conf connect Test*
