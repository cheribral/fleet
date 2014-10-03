package agent

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/coreos/fleet/job"
	"github.com/coreos/fleet/log"
	"github.com/coreos/fleet/machine"
)

type AgentState struct {
	MState *machine.MachineState
	Units  map[string]*job.Unit
}

func NewAgentState(ms *machine.MachineState) *AgentState {
	return &AgentState{
		MState: ms,
		Units:  make(map[string]*job.Unit),
	}
}

func (as *AgentState) unitScheduled(name string) bool {
	return as.Units[name] != nil
}

func (as *AgentState) allocatedCPUUnits() float64 {
	allocated := float64(0.0)
	for _, eUnit := range as.Units {
		allocated += eUnit.RequestedCPUUnits()
	}
	return allocated
}

// hasConflict determines whether there are any known conflicts with the given Unit
func (as *AgentState) hasConflict(pUnitName string, pConflicts []string) (found bool, conflict string) {
	for _, eUnit := range as.Units {
		if pUnitName == eUnit.Name {
			continue
		}

		for _, pConflict := range pConflicts {
			if globMatches(pConflict, eUnit.Name) {
				found = true
				conflict = eUnit.Name
				return
			}
		}

		for _, eConflict := range eUnit.Conflicts() {
			if globMatches(eConflict, pUnitName) {
				found = true
				conflict = eUnit.Name
				return
			}
		}
	}

	return
}

func globMatches(pattern, target string) bool {
	matched, err := path.Match(pattern, target)
	if err != nil {
		log.V(1).Infof("Received error while matching pattern '%s': %v", pattern, err)
	}
	return matched
}

// AbleToRun determines if an Agent can run the provided Job based on
// the Agent's current state. A boolean indicating whether this is the
// case or not is returned. The following criteria is used:
//   - Agent must meet the Job's machine target requirement (if any)
//   - Agent must have all of the Job's required metadata (if any)
//   - Agent must have all required Peers of the Job scheduled locally (if any)
//   - Job must not conflict with any other Units scheduled to the agent
func (as *AgentState) AbleToRun(j *job.Job) (bool, string) {
	if tgt, ok := j.RequiredTarget(); ok && !as.MState.MatchID(tgt) {
		return false, fmt.Sprintf("agent ID %q does not match required %q", as.MState.ID, tgt)
	}

	metadata := j.RequiredTargetMetadata()
	if len(metadata) != 0 {
		if !machine.HasMetadata(as.MState, metadata) {
			return false, "local Machine metadata insufficient"
		}
	}

	peers := j.Peers()
	if len(peers) != 0 {
		for _, peer := range peers {
			if !as.unitScheduled(peer) {
				return false, fmt.Sprintf("required peer Unit(%s) is not scheduled locally", peer)
			}
		}
	}

	if cExists, cJobName := as.hasConflict(j.Name, j.Conflicts()); cExists {
		return false, fmt.Sprintf("found conflict with locally-scheduled Unit(%s)", cJobName)
	}
	memfile, err := os.Open("/proc/meminfo")
	if err != nil {
		return false, "Could not read meminfo"
	}
	defer memfile.Close()
	scanner := bufio.NewScanner(memfile)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "MemAvailable") {
			mem, _ := strconv.Atoi(strings.Fields(scanner.Text())[1])
			if mem < j.NeededMemory() {
				log.Infof(fmt.Sprintf("Not enough memory to run %s.  I am short by %d", j.Name, j.NeededMemory()-mem))
				return false, "Not enough memory to run unit"
			}
		}
	}
	cpuinfo, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return false, "Could not read cpuinfo"
	}
	defer cpuinfo.Close()
	scanner = bufio.NewScanner(cpuinfo)
	cpus := float64(0)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "processor") {
			cpus += 1
		}
	}
	unallocated := cpus - as.allocatedCPUUnits()
	if unallocated < j.RequestedCPUUnits() {
		log.Infof(fmt.Sprintf("Not enough CPUUnits left to allocate for %s, short by %v", j.Name, j.RequestedCPUUnits()-(cpus-as.allocatedCPUUnits())))
		return false, "Not enough CPU units left to allocate"
	}

	return true, ""
}
